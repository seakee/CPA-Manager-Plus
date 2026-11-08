#!/usr/bin/env python3
"""Disposable linux/amd64 bundle smoke; no production mounts or image push.

Build first (the Dockerfile fails closed without both identity arguments):
  docker buildx build --platform linux/amd64 --target full \
    --build-arg VERSION=dev --build-arg SOURCE_COMMIT="$(git rev-parse HEAD)" \
    -f Dockerfile --load -t cpamp-d1-runtime:local .
Then: python3 bin/ci/runtime-v8-bundle-smoke.py IMAGE NEW_EVIDENCE_DIRECTORY
Uses a Python probe on a unique internal Docker network; no ports are published.
This is bundle evidence, not Fresh/Upgrade or Beta acceptance.
"""

import hashlib
import json
import os
import pathlib
import secrets
import subprocess
import sys
import time


PIN = "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
ROOT = pathlib.Path(__file__).resolve().parents[2]


def main():
    argv = sys.argv[1:]
    if len(argv) != 2:
        raise SystemExit("Usage: runtime-v8-bundle-smoke.py IMAGE NEW_EVIDENCE_DIRECTORY")
    image, output = argv
    out = pathlib.Path(output).resolve()
    out.mkdir(parents=True, exist_ok=False)
    name = os.environ.get("CPAMP_SMOKE_PROJECT") or ("cpamp-d1-" + secrets.token_hex(6))
    token = "d1-disposable-runtime-transport-token-only"
    management_key = secrets.token_hex(32)
    client_key = secrets.token_hex(32)
    result = {"scope": "linux/amd64 isolated Runtime candidate", "result": "FAIL"}
    events = []
    probe = name + "-probe"
    def command(*args, check=True):
        completed = subprocess.run(args, capture_output=True, text=True, timeout=120)
        events.append({"command": list(args), "exitCode": completed.returncode,
                       "stdout": completed.stdout, "stderr": completed.stderr})
        if check and completed.returncode:
            raise RuntimeError(f"command failed: {args}: {completed.stderr}")
        return completed.stdout.strip()

    def request(base, path, method="GET", body=None, key=None, expected=200, local=False):
        headers = {"X-CPAMP-Runtime-Features": "active-gateway-artifact-v1"}
        if key is not None:
            headers["Authorization"] = "Bearer " + key
        if body is not None:
            headers["Content-Type"] = "application/json"
        # Local requests run inside the target container's network namespace for
        # loopback-only listeners (9081/8317); non-local requests run from the
        # peer probe container on the internal Docker network.
        program = '''import json, sys, urllib.request, urllib.error
v = json.load(sys.stdin)
req = urllib.request.Request(v['url'], method=v['method'], headers=v['headers'],
    data=json.dumps(v['body']).encode() if v['body'] is not None else None)
try:
    response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=3)
except urllib.error.HTTPError as e:
    response = e
except Exception as e:
    print(json.dumps({'code': 0, 'version': None, 'body': str(e)}))
    raise SystemExit(0)
with response:
    print(json.dumps({'code': response.code, 'version': response.headers.get('X-CPA-VERSION'),
                      'body': response.read(1 << 20).decode()}))
'''
        args = (["docker", "run", "--rm", "-i", "--network", "container:" + name,
                 result["probeImageId"], "python3", "-c", program] if local else
                ["docker", "exec", "-i", probe, "python3", "-c", program])
        completed = subprocess.run(args, input=json.dumps({"url": base + path, "method": method,
                                   "headers": headers, "body": body}),
                                   capture_output=True, text=True, timeout=15)
        events.append({"command": args, "input": "disposable HTTP request; credentials omitted",
                       "exitCode": completed.returncode, "stderr": completed.stderr})
        if completed.returncode:
            raise OSError("probe failed: " + completed.stderr)
        response = json.loads(completed.stdout)
        data = response["body"].encode()
        code, version = response["code"], response["version"]
        events.append({"http": method + " " + path, "status": code,
                       "expected": expected, "cpaVersion": version,
                       "responseSha256": hashlib.sha256(data).hexdigest()})
        assert code == expected, (method, path, code, expected, response["body"])
        try:
            value = json.loads(data) if data else None
        except json.JSONDecodeError:
            value = data.decode()
        return value, version

    def wait_status(wanted=None):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                status, _ = request(runtime_url, "/v1/runtime/status", key=token, local=True)
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
                           "POST", body, token, expected, local=True)
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
        runtime_url = "http://127.0.0.1:9081"
        cpa_url = "http://127.0.0.1:8317"
        ingress_url = "http://" + name + ":18317"
        result["ingressImageId"] = metadata["Id"]
        status = wait_status("offline")
        assert status["activeGatewayArtifact"]["artifactId"] == "sha256:" + PIN
        request(runtime_url, "/v1/runtime/status", expected=401, local=True)
        request("http://" + name + ":9081", "/v1/runtime/status", key=token, expected=0)
        manifest = json.loads(command("docker", "exec", name, "cat",
                                      "/usr/local/share/cpamp/runtime/cpa-artifact.json"))
        assert manifest == {"schemaVersion": 1, "engine": "cpa", "version": "8.0.55",
                            "artifactId": "sha256:" + PIN}
        result["manifest"] = manifest
        hashes = command("docker", "exec", name, "sha256sum", "/usr/local/bin/cli-proxy-api",
                         "/usr/local/bin/cpamp-runtime-supervisor",
                         "/usr/local/share/cpamp/runtime/config.seed.yaml",
                         "/data/runtime/gateway/config.yaml")
        result["bootstrapHashes"] = hashes
        seed_hash = hashlib.sha256((ROOT / "docker/runtime/config.v8.default.seed.yaml").read_bytes()).hexdigest()
        assert hashes.splitlines()[0].split()[0] == PIN
        assert all(line.split()[0] == seed_hash for line in hashes.splitlines()[2:])
        command("docker", "exec", name, "sh", "-ec", '''
test "$(stat -c '%u:%g:%a' /usr/local/share/cpamp/runtime/cpa-artifact.json)" = 0:0:444
test "$(stat -c '%u:%g:%a' /data/runtime/gateway)" = 10001:10001:700
test "$(stat -c '%u:%g:%a' /data/runtime/gateway/config.yaml)" = 10001:10001:600
test "$(stat -c '%u:%g:%a' /data/runtime-secret)" = 0:10002:750
test "$(stat -c '%u:%g:%a' /data/runtime-secret/token)" = 0:10002:640
test "$(stat -c '%u:%g:%a' /data/runtime/supervisor)" = 0:10001:710
''')
        mutate("start", "d1-bootstrap-start")
        wait_status("ready")
        process = snapshot("bootstrap")
        assert "-config" not in process and "/data/runtime/gateway" in process
        request(cpa_url, "/healthz", "HEAD", local=True)
        request(cpa_url, "/v8/management/config", expected=404, local=True)
        request(cpa_url, "/v0/management/config", expected=404, local=True)
        request(cpa_url, "/v1/models", local=True)
        request("http://" + name + ":8317", "/v1/models", expected=0)
        pending, _ = request(ingress_url, "/v1/models", expected=503)
        assert pending.strip() == "gateway pending configuration"
        result["bootstrapSecurity"] = "empty access.api-keys: no-key /v1/models=200 on loopback; Ingress pending; CPA loopback-only"
        mutate("stop", "d1-bootstrap-stop")
        wait_status("offline")
        stage = {"sourceIdentity": "external:sha256:" + "a" * 64,
                 "sourceClass": "network_existing", "apiKeys": [client_key],
                 "managementKey": management_key}
        mutate("stage-destination", "d1-stage", stage)
        mutate("stage-destination", "d1-stage", stage)
        mutate("start", "d1-destination-start")
        wait_status("ready")
        assert "-config /data/runtime/supervisor/destination/config.yaml" in snapshot("destination")
        config, version = request(cpa_url, "/v8/management/config", key=management_key, local=True)
        assert version == "8.0.55", version
        assert config["config-version"] == 8, config.keys()
        assert config["access"]["api-keys"] == [client_key]
        assert config["server"]["host"] == "127.0.0.1" and config["server"]["port"] == 8317, config["server"]
        assert config["management"]["allow-remote"] is False, config["management"]
        assert config["oauth"]["auth-dir"] == "/data/runtime/supervisor/destination/auth"
        result["configVersion"] = config["config-version"]
        result["cpaVersion"] = version
        request(cpa_url, "/v8/management/config", expected=401, local=True)
        request(cpa_url, "/v1/models", expected=401, local=True)
        request(cpa_url, "/v1/models", key=management_key, expected=401, local=True)
        request(cpa_url, "/v1/models", key=client_key, local=True)
        request("http://" + name + ":8317", "/v1/models", key=client_key, expected=0)
        request(ingress_url, "/v1/models", key=client_key, expected=503)
        result["ingress"] = "uninitialized Manager/Fresh authority: Gateway remains 503; CPA remains loopback-only"
        # CPA's UID cannot alter the manifest/journal or read the transport/stage secret.
        command("docker", "exec", "--user", "10001:10001", name, "sh", "-ec", '''
test ! -w /usr/local/share/cpamp/runtime/cpa-artifact.json
test ! -w /data/runtime/supervisor
test ! -r /data/runtime/supervisor/destination/stage.json
test ! -r /data/runtime-secret/token
test -r /data/runtime/supervisor/destination/config.yaml
test -w /data/runtime/supervisor/destination/auth
''')
        mutate("restart", "d1-child-restart")
        wait_status("ready")
        snapshot("child-restarted")
        request(cpa_url, "/v1/models", key=client_key, local=True)
        previous_generation = wait_status()["runtimeGeneration"]
        command("docker", "restart", "--time", "15", name)
        status = wait_status()
        assert status["runtimeGeneration"] != previous_generation
        mutate("start", "d1-stale", expected=409, generation=previous_generation)
        mutate("start", "d1-after-container-restart")
        wait_status("ready")
        snapshot("container-restarted")
        config, _ = request(cpa_url, "/v8/management/config", key=management_key, local=True)
        assert config["access"]["api-keys"] == [client_key]
        request(cpa_url, "/v1/models", expected=401, local=True)
        request(cpa_url, "/v1/models", key=client_key, local=True)
        result["stagedConfigSha256"] = command("docker", "exec", name, "sha256sum",
                                               "/data/runtime/supervisor/destination/config.yaml")
        result["result"] = "PASS"
        result["openGates"] = ["five Fresh/Upgrade journeys", "Beta release"]
    finally:
        command("docker", "logs", name, check=False)
        command("docker", "rm", "-f", name, check=False)
        command("docker", "rm", "-f", probe, check=False)
        command("docker", "network", "rm", name, check=False)
        (out / "commands.json").write_text(json.dumps(events, indent=2) + "\n")
        (out / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
