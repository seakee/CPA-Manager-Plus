#!/usr/bin/env python3
"""Disposable linux/amd64 bundle smoke; no production mounts or image push.

Build first (the Dockerfiles fail closed without both identity arguments):
  docker buildx build --platform linux/amd64 --target v8-amd64-candidate \
    --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$(git rev-parse HEAD)" \
    -f Dockerfile.runtime --load -t cpamp-d1-runtime:local .
Also build Dockerfile.ingress with --platform linux/amd64 --load and the same
two --build-arg values.
Then: python3 bin/ci/runtime-v8-bundle-smoke.py RUNTIME_IMAGE INGRESS_IMAGE NEW_EVIDENCE_DIRECTORY
Uses a Python probe on a unique internal Docker network; no ports are published.
This is bundle evidence, not Fresh/Upgrade or Beta acceptance.
"""

import hashlib
import json
import pathlib
import secrets
import subprocess
import sys
import time


PIN = "b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41"
ROOT = pathlib.Path(__file__).resolve().parents[2]


def main():
    image, ingress_image, output = sys.argv[1:]
    out = pathlib.Path(output).resolve()
    out.mkdir(parents=True, exist_ok=False)
    name = "cpamp-d1-" + secrets.token_hex(6)
    token = "d1-disposable-runtime-transport-token-only"
    management_key = secrets.token_hex(32)
    client_key = secrets.token_hex(32)
    result = {"scope": "linux/amd64 isolated Runtime candidate", "result": "FAIL"}
    events = []
    probe = name + "-probe"
    ingress = name + "-ingress"

    def command(*args, check=True):
        completed = subprocess.run(args, capture_output=True, text=True, timeout=120)
        events.append({"command": list(args), "exitCode": completed.returncode,
                       "stdout": completed.stdout, "stderr": completed.stderr})
        if check and completed.returncode:
            raise RuntimeError(f"command failed: {args}: {completed.stderr}")
        return completed.stdout.strip()

    def request(base, path, method="GET", body=None, key=None, expected=200):
        headers = {"X-CPAMP-Runtime-Features": "active-gateway-artifact-v1"}
        if key is not None:
            headers["Authorization"] = "Bearer " + key
        if body is not None:
            headers["Content-Type"] = "application/json"
        # The probe is inside the private network; Docker Desktop does not
        # publish ports on --internal networks. Input stays off the command log.
        program = '''import json, sys, urllib.request, urllib.error
v = json.load(sys.stdin)
req = urllib.request.Request(v['url'], method=v['method'], headers=v['headers'],
    data=json.dumps(v['body']).encode() if v['body'] is not None else None)
try:
    response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=3)
except urllib.error.HTTPError as e:
    response = e
with response:
    print(json.dumps({'code': response.code, 'version': response.headers.get('X-CPA-VERSION'),
                      'body': response.read(1 << 20).decode()}))
'''
        args = ["docker", "exec", "-i", probe, "python3", "-c", program]
        completed = subprocess.run(args, input=json.dumps({"url": base + path, "method": method,
                                   "headers": headers, "body": body}),
                                   capture_output=True, text=True, timeout=10)
        events.append({"command": args, "input": "disposable HTTP request; credentials omitted",
                       "exitCode": completed.returncode, "stderr": completed.stderr})
        if completed.returncode:
            raise OSError("probe failed: " + completed.stderr)
        response = json.loads(completed.stdout)
        data = response["body"].encode()
        code, version = response["code"], response["version"]
        # Do not store raw config/credentials, even these disposable test keys.
        events.append({"http": method + " " + path, "status": code,
                       "expected": expected, "cpaVersion": version,
                       "responseSha256": hashlib.sha256(data).hexdigest()})
        assert code == expected, (method, path, code, expected)
        try:
            value = json.loads(data) if data else None
        except json.JSONDecodeError:
            value = data.decode()
        return value, version

    def wait_status(wanted=None):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                status, _ = request(runtime_url, "/v1/runtime/status", key=token)
                if wanted is None or status["state"] == wanted:
                    return status
            except (OSError, AssertionError):
                pass
            time.sleep(0.2)
        raise RuntimeError(f"Supervisor did not reach {wanted}")

    def mutate(action, operation, extra=None, expected=200, generation=None):
        status = wait_status()
        body = {"operationId": operation, "expectedRuntimeIdentity": status["runtimeIdentity"],
                "expectedRuntimeGeneration": status["runtimeGeneration"] if generation is None else generation}
        if action in ("start", "stage-destination"):
            body["expectedActiveArtifactId"] = "sha256:" + PIN
        body.update(extra or {})
        value, _ = request(runtime_url, "/v1/runtime/operations/" + action,
                           "POST", body, token, expected)
        if expected == 200:
            assert value["state"] == "succeeded", value
        return value

    def snapshot(label):
        # Inspect the live executable, command, working directory and credentials.
        value = command("docker", "exec", "--user", "10001:10001", name, "sh", "-ec", r'''
for p in /proc/[0-9]*; do
  [ -r "$p/comm" ] || continue
  [ "$(cat "$p/comm")" = cli-proxy-api ] || continue
  sha256sum "$p/exe"
  tr '\000' ' ' < "$p/cmdline"
  printf '\n'
  readlink "$p/cwd"
  sed -n '/^Uid:/p; /^Gid:/p' "$p/status"
  exit 0
done
exit 1
''')
        assert value.split()[0] == PIN, value
        assert "Uid:\t10001\t10001\t10001\t10001" in value, value
        assert "Gid:\t10001\t10001\t10001\t10001" in value, value
        (out / (label + "-process.txt")).write_text(value + "\n")
        return value

    try:
        metadata = json.loads(command("docker", "image", "inspect", image))[0]
        assert metadata["Os"] == "linux" and metadata["Architecture"] == "amd64"
        result["imageId"] = metadata["Id"]
        result["repoDigests"] = metadata["RepoDigests"]
        # Resolve the tool before entering the egress-free test network.
        tool = command("docker", "image", "inspect", "python:3.13-alpine", check=False)
        if not tool:
            command("docker", "pull", "python:3.13-alpine")
            tool = command("docker", "image", "inspect", "python:3.13-alpine")
        result["probeImageId"] = json.loads(tool)[0]["Id"]
        command("docker", "network", "create", "--internal", name)
        command("docker", "run", "-d", "--name", probe, "--network", name,
                result["probeImageId"], "python3", "-c", "import time; time.sleep(600)")
        command("docker", "run", "-d", "--name", name, "--network", name,
                "--platform", "linux/amd64", "-e", "CPAMP_RUNTIME_TOKEN=" + token,
                metadata["Id"])
        inspect = json.loads(command("docker", "inspect", name))[0]
        assert inspect["Mounts"] == [], "smoke must not mount user data"
        assert not inspect["HostConfig"]["PortBindings"]
        runtime_url = "http://" + name + ":9081"
        cpa_url = "http://" + name + ":8317"
        ingress_meta = json.loads(command("docker", "image", "inspect", ingress_image))[0]
        assert ingress_meta["Os"] == "linux" and ingress_meta["Architecture"] == "amd64"
        result["ingressImageId"] = ingress_meta["Id"]
        # No Manager is running. Unknown Fresh authority must keep Gateway
        # closed even when the bundled CPA has no bootstrap client keys.
        command("docker", "run", "-d", "--name", ingress, "--network", name,
                "-e", "CPAMP_MANAGER_URL=http://127.0.0.1:1",
                "-e", "CPAMP_GATEWAY_URL=" + cpa_url, ingress_meta["Id"])
        ingress_url = "http://" + ingress + ":18317"
        status = wait_status("offline")
        assert status["activeGatewayArtifact"]["artifactId"] == "sha256:" + PIN
        request(runtime_url, "/v1/runtime/status", expected=401)
        manifest = json.loads(command("docker", "exec", name, "cat",
                                     "/usr/local/share/cpamp/runtime/cpa-artifact.json"))
        assert manifest == {"schemaVersion": 1, "engine": "cpa", "version": "8.0.13",
                            "artifactId": "sha256:" + PIN}
        result["manifest"] = manifest
        hashes = command("docker", "exec", name, "sha256sum", "/usr/local/bin/cli-proxy-api",
                         "/usr/local/bin/cpamp-runtime-supervisor",
                         "/usr/local/share/cpamp/runtime/config.seed.yaml",
                         "/runtime/gateway/config.yaml")
        result["bootstrapHashes"] = hashes
        seed_hash = hashlib.sha256((ROOT / "docker/runtime/config.v8.seed.yaml").read_bytes()).hexdigest()
        assert hashes.splitlines()[0].split()[0] == PIN
        assert all(line.split()[0] == seed_hash for line in hashes.splitlines()[2:])
        command("docker", "exec", name, "sh", "-ec", '''
test "$(stat -c '%u:%g:%a' /usr/local/share/cpamp/runtime/cpa-artifact.json)" = 0:0:444
test "$(stat -c '%u:%g:%a' /runtime/gateway)" = 10001:10001:700
test "$(stat -c '%u:%g:%a' /runtime/gateway/config.yaml)" = 10001:10001:600
test "$(stat -c '%u:%g:%a' /run/cpamp/runtime-secret/token)" = 0:0:600
test "$(stat -c '%u:%g:%a' /runtime/supervisor)" = 0:10001:710
''')
        mutate("start", "d1-bootstrap-start")
        wait_status("ready")
        process = snapshot("bootstrap")
        assert "-config" not in process and "/runtime/gateway" in process
        request(cpa_url, "/healthz", "HEAD")
        request(cpa_url, "/v8/management/config", expected=404)
        request(cpa_url, "/v0/management/config", expected=404)
        request(cpa_url, "/v1/models")
        pending, _ = request(ingress_url, "/v1/models", expected=503)
        assert pending.strip() == "gateway pending configuration"
        result["bootstrapSecurity"] = "empty access.api-keys: no-key /v1/models=200; isolated only; C1/security gate OPEN"
        mutate("stop", "d1-bootstrap-stop")
        wait_status("offline")
        stage = {"sourceIdentity": "external:sha256:" + "a" * 64,
                 "sourceClass": "network_existing", "apiKeys": [client_key],
                 "managementKey": management_key}
        mutate("stage-destination", "d1-stage", stage)
        mutate("stage-destination", "d1-stage", stage)
        mutate("start", "d1-destination-start")
        wait_status("ready")
        assert "-config /runtime/supervisor/destination/config.yaml" in snapshot("destination")
        config, version = request(cpa_url, "/v8/management/config", key=management_key)
        assert version == "8.0.13", version
        assert config["config-version"] == 8, config.keys()
        assert config["access"]["api-keys"] == [client_key]
        assert config["server"]["host"] == "0.0.0.0" and config["server"]["port"] == 8317
        assert config["oauth"]["auth-dir"] == "/runtime/supervisor/destination/auth"
        result["configVersion"] = config["config-version"]
        result["cpaVersion"] = version
        request(cpa_url, "/v8/management/config", expected=401)
        request(cpa_url, "/v1/models", expected=401)
        request(cpa_url, "/v1/models", key=management_key, expected=401)
        request(cpa_url, "/v1/models", key=client_key)
        request(ingress_url, "/v1/models", key=client_key, expected=503)
        result["ingress"] = "unknown Manager/Fresh authority: Gateway remains 503; full deployment NOT RUN"
        # CPA's UID cannot alter the manifest/journal or read the transport/stage secret.
        command("docker", "exec", "--user", "10001:10001", name, "sh", "-ec", '''
test ! -w /usr/local/share/cpamp/runtime/cpa-artifact.json
test ! -w /runtime/supervisor
test ! -r /runtime/supervisor/destination/stage.json
test ! -r /run/cpamp/runtime-secret/token
test -r /runtime/supervisor/destination/config.yaml
test -w /runtime/supervisor/destination/auth
''')
        mutate("restart", "d1-child-restart")
        wait_status("ready")
        snapshot("child-restarted")
        request(cpa_url, "/v1/models", key=client_key)
        previous_generation = wait_status()["runtimeGeneration"]
        command("docker", "restart", "--time", "15", name)
        status = wait_status()
        assert status["runtimeGeneration"] != previous_generation
        mutate("start", "d1-stale", expected=409, generation=previous_generation)
        mutate("start", "d1-after-container-restart")
        wait_status("ready")
        snapshot("container-restarted")
        config, _ = request(cpa_url, "/v8/management/config", key=management_key)
        assert config["access"]["api-keys"] == [client_key]
        request(cpa_url, "/v1/models", expected=401)
        request(cpa_url, "/v1/models", key=client_key)
        result["stagedConfigSha256"] = command("docker", "exec", name, "sha256sum",
                                               "/runtime/supervisor/destination/config.yaml")
        result["result"] = "PASS"
        result["openGates"] = ["C1/security", "five Fresh/Upgrade journeys", "license/NOTICES",
                               "Readiness", "Full Web/Manager/Ingress deployment pairing", "Beta release"]
    finally:
        command("docker", "logs", name, check=False)
        command("docker", "rm", "-f", name, check=False)
        command("docker", "rm", "-f", probe, check=False)
        command("docker", "logs", ingress, check=False)
        command("docker", "rm", "-f", ingress, check=False)
        command("docker", "network", "rm", name, check=False)
        (out / "commands.json").write_text(json.dumps(events, indent=2) + "\n")
        (out / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
