#!/usr/bin/env python3
"""Normal Compose v8 bootstrap test with disposable images/volumes only.
Usage: runtime-v8-default-smoke.py IMAGE NEW_OUT
Build Dockerfile (--target full or default). Requires local python:3.13-alpine.
"""
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
PINS = {
    "amd64": "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7",
    "arm64": "4a5b43cc3700d36c72b7c1953e9b11eca0888c13b789ea2f99471117504a7d5d",
}
PROBE = r"""
import json,sys,urllib.request,urllib.error
v=json.load(sys.stdin)
h={'X-CPAMP-Runtime-Features':'active-gateway-artifact-v1'}
if v.get('key'):h['Authorization']='Bearer '+v['key']
b=json.dumps(v['body']).encode() if v.get('body') is not None else None
if b is not None:h['Content-Type']='application/json'
r=urllib.request.Request(v['url'],data=b,method=v.get('method','GET'),headers=h)
try:
 r=urllib.request.build_opener(urllib.request.ProxyHandler({})).open(r,timeout=10)
except urllib.error.HTTPError as e:r=e
except urllib.error.URLError:
 print(json.dumps({'code':0}));sys.exit(0)
with r:print(json.dumps({'code':r.code,'body':r.read(1<<20).decode()}))
"""


def main():
    argv = sys.argv[1:]
    if len(argv) != 2:
        raise SystemExit("Usage: runtime-v8-default-smoke.py IMAGE NEW_OUT")
    runtime_image, output = argv
    out = Path(output).resolve()
    out.mkdir(parents=True, exist_ok=False)
    project = os.environ.get("CPAMP_SMOKE_PROJECT") or ("cpamp-default-v8-" + secrets.token_hex(6))
    probe = project + "-peer"
    token = secrets.token_hex(32)
    result = {"result": "FAIL", "scope": "normal Compose v8 bootstrap, not public Gateway qualification"}
    events, volumes = [], []
    env = dict(os.environ, CPAMP_PUBLIC_PORT=os.environ.get("CPAMP_PUBLIC_PORT", "127.0.0.1:0"))

    def cmd(*args, data=None, check=True):
        p = subprocess.run(args, input=data, capture_output=True, text=True,
                           timeout=180, cwd=ROOT, env=env)
        if check and p.returncode:
            raise RuntimeError(f"{args[:3]} failed: {p.stderr}")
        return p

    def inspect(image):
        return json.loads(cmd("docker", "image", "inspect", image).stdout)[0]

    meta = inspect(runtime_image)
    assert meta["Os"] == "linux" and meta["Architecture"] in PINS
    arch, pin = meta["Architecture"], PINS[meta["Architecture"]]
    images = {"cpa-manager-plus": meta["Id"]}
    probe_image = inspect("python:3.13-alpine")["Id"]
    result.update(architecture=arch, imageIds=images, repoDigests=meta["RepoDigests"])
    override = out / "compose.override.json"
    override.write_text(json.dumps({"services": {
        "cpa-manager-plus": {
            "image": meta["Id"],
            "platform": "linux/" + arch,
            "environment": {"CPAMP_RUNTIME_TOKEN": token},
        }
    }}))
    compose = ["docker", "compose", "--project-directory", str(ROOT), "-p", project,
               "-f", str(ROOT / "docker-compose.yml"), "-f", str(override)]

    def dc(*args, **kw):
        return cmd(*compose, *args, **kw)
    def http(url, key=None, body=None, method="GET", local=False):
        args = (["docker", "run", "--rm", "-i", "--network", "container:"+runtime,
                 probe_image, "python3", "-c", PROBE] if local else
                ["docker", "exec", "-i", probe, "python3", "-c", PROBE])
        v = json.loads(cmd(*args, data=json.dumps(dict(url=url, key=key, body=body, method=method))).stdout)
        events.append({"url": url, "method": method, "key": "present" if key else "absent",
                       "status": v["code"], "runtimeNamespace": local})
        return v

    def wait_state(state=None):
        for _ in range(120):
            v = http("http://127.0.0.1:9081/v1/runtime/status", token, local=True)
            if v["code"] == 200:
                s = json.loads(v["body"])
                if state is None or s["state"] == state:
                    return s
            time.sleep(0.25)
        raise AssertionError("Runtime did not reach " + str(state))

    def mutate(action, generation=None, expected=200):
        s = wait_state()
        b = {"operationId": "default-v8:"+secrets.token_hex(8),
             "expectedRuntimeIdentity": s["runtimeIdentity"],
             "expectedRuntimeGeneration": s["runtimeGeneration"] if generation is None else generation}
        if action == "start":
            b["expectedActiveArtifactId"] = "sha256:"+pin
        v = http("http://127.0.0.1:9081/v1/runtime/operations/"+action, token, b, "POST", local=True)
        assert v["code"] == expected, v
        if expected == 200:
            assert json.loads(v["body"])["state"] == "succeeded", v

    def closed(label):
        for path, method in [("/v1/models", "GET"), ("/v1/chat/completions", "POST"),
                             ("/v1/responses", "POST"), ("/v1/messages", "POST"),
                             ("/v1beta/models", "GET")]:
            for key in (None, "incorrect-client-key"):
                v = http("http://cpa-manager-plus:18317"+path, key,
                         {"model": "no-provider", "messages": []} if method == "POST" else None, method)
                assert v["code"] == 503 and v["body"].strip() == "gateway pending configuration", v
        for key in (None, "incorrect-client-key"):
            assert http("http://cpa-manager-plus:8317/v1/models", key)["code"] == 0
            assert http("http://cpa-manager-plus:9081/v1/runtime/status", key)["code"] == 0
        events.append({"boundary": label, "result": "PASS"})

    def volume_snapshot(volume):
        return cmd("docker", "run", "--rm", "--entrypoint", "sh", "-v", volume+":/runtime:ro",
                   meta["Id"], "-ec", "find /runtime -exec stat -c '%n %u:%g %a %N' {} + | sort; find /runtime -type f -exec sha256sum {} + | sort").stdout

    try:
        dc("up", "-d", "--no-build", "--pull", "never")
        runtime = dc("ps", "-q", "cpa-manager-plus").stdout.strip()
        runtime_meta = json.loads(cmd("docker", "inspect", runtime).stdout)[0]
        assert set(runtime_meta["HostConfig"]["PortBindings"]) == {"18317/tcp"}
        network = next(iter(runtime_meta["NetworkSettings"]["Networks"]))
        cmd("docker", "run", "-d", "--name", probe, "--network", network,
            probe_image, "sleep", "1800")
        token_file = "/data/runtime-secret/token"
        for _ in range(120):
            p_tok = dc("exec", "-T", "cpa-manager-plus", "sh", "-ec",
                       "for c in /data/runtime-secret/token /data/.cpamp-runtime-secret/token /run/cpamp/runtime-secret/token; do if [ -s \"$c\" ]; then printf '%s\\n' \"$c\"; cat \"$c\"; exit 0; fi; done; exit 1",
                       check=False)
            if p_tok.returncode == 0:
                lines = p_tok.stdout.splitlines()
                token_file = lines[0].strip()
                token = lines[1].strip() if len(lines) > 1 else token
                break
            time.sleep(0.25)
        initial = wait_state("offline")
        assert initial["activeGatewayArtifact"]["version"] == "8.0.55"
        assert initial["activeGatewayArtifact"]["artifactId"] == "sha256:"+pin
        assert http("http://127.0.0.1:9081/v1/runtime/status", local=True)["code"] == 401
        manifest = json.loads(dc("exec", "-T", "cpa-manager-plus", "cat",
                                 "/usr/local/share/cpamp/runtime/cpa-artifact.json").stdout)
        assert manifest == {"schemaVersion": 1, "engine": "cpa", "version": "8.0.55", "artifactId": "sha256:"+pin}
        runtime_root = dc("exec", "-T", "cpa-manager-plus", "sh", "-ec",
                          "if [ -f /data/runtime/gateway/config.yaml ]; then printf /data/runtime; else printf /runtime; fi").stdout.strip()
        hashes = dc("exec", "-T", "cpa-manager-plus", "sha256sum", "/usr/local/bin/cli-proxy-api",
                    "/usr/local/share/cpamp/runtime/config.seed.yaml", f"{runtime_root}/gateway/config.yaml").stdout
        assert hashes.splitlines()[0].split()[0] == pin
        seed_hash = hashlib.sha256((ROOT/"docker/runtime/config.v8.default.seed.yaml").read_bytes()).hexdigest()
        assert all(line.split()[0] == seed_hash for line in hashes.splitlines()[1:])
        result.update(manifest=manifest, hashes=hashes)
        closed("offline/unknown")
        mutate("start")
        wait_state("ready")
        assert http("http://127.0.0.1:8317/healthz", method="HEAD", local=True)["code"] == 200
        live = dc("exec", "-T", "--user", "10001:10001", "cpa-manager-plus", "sh", "-ec", r"""
for p in /proc/[0-9]*; do
  [ -r "$p/comm" ] || continue
  [ "$(cat "$p/comm")" = cli-proxy-api ] || continue
  sha256sum "$p/exe"
  sed -n '/^Uid:/p; /^Gid:/p' "$p/status"
  exit 0
done
exit 1
""").stdout
        assert live.split()[0] == pin and "Uid:\t10001\t10001\t10001\t10001" in live
        assert "Gid:\t10001\t10001\t10001\t10001" in live
        result["liveProcess"] = live
        dc("exec", "-T", "cpa-manager-plus", "sh", "-ec",
           "test \"$(stat -c '%u:%g:%a' /usr/local/share/cpamp/runtime/cpa-artifact.json)\" = 0:0:444")
        dc("exec", "-T", "--user", "10001:10001", "cpa-manager-plus", "sh", "-ec",
           f"test ! -w /usr/local/share/cpamp/runtime/cpa-artifact.json; test ! -w {runtime_root}/supervisor; test ! -r {token_file}")
        dc("exec", "-T", "--user", "10003:10003", "cpa-manager-plus", "sh", "-ec",
           f"test ! -r {token_file}; test ! -w {runtime_root}/supervisor")
        closed("ready/no-client-keys")
        mutate("restart")
        wait_state("ready")
        closed("CPA restarted")
        generation = wait_state()["runtimeGeneration"]
        dc("exec", "-T", "cpa-manager-plus", "sh", "-ec", "kill -TERM $(pidof cpamp-runtime-supervisor)")
        for _ in range(120):
            s = wait_state("offline")
            if s["runtimeGeneration"] != generation:
                break
            time.sleep(0.25)
        assert wait_state("offline")["runtimeGeneration"] != generation
        mutate("start", generation=generation, expected=409)
        closed("new generation/stale authority")
        mutate("start")
        wait_state("ready")
        closed("Runtime restarted")
        mutate("stop")
        wait_state("offline")
        closed("CPA unavailable")
        dc("exec", "-T", "cpa-manager-plus", "sh", "-ec", "kill -TERM $(pidof cpa-manager-plus) $(pidof cpamp-ingress)")
        for _ in range(60):
            if http("http://cpa-manager-plus:18317/v1/models")["code"] == 503:
                break
            time.sleep(0.25)
        closed("Manager unavailable/Ingress restarted")
        address = dc("port", "cpa-manager-plus", "18317").stdout.strip()
        assert address.startswith("127.0.0.1:")
        import urllib.error
        import urllib.request
        try:
            urllib.request.build_opener(urllib.request.ProxyHandler({})).open("http://"+address+"/v1/models", timeout=10)
            raise AssertionError("host endpoint admitted no-key request")
        except urllib.error.HTTPError as e:
            assert e.code == 503
        for label, content in [("v7", (ROOT/"docker/runtime/config.seed.yaml").read_text()),
                               ("unknown-v8", (ROOT/"docker/runtime/config.v8.default.seed.yaml").read_text())]:
            volume = project+"-"+label
            cmd("docker", "volume", "create", volume)
            volumes.append(volume)
            cmd("docker", "run", "--rm", "-i", "--entrypoint", "sh", "-v", volume+":/runtime",
                meta["Id"], "-ec", "mkdir /runtime/gateway; cat > /runtime/gateway/config.yaml; chmod 640 /runtime/gateway/config.yaml", data=content)
            before = volume_snapshot(volume)
            rejected = cmd("docker", "run", "--rm", "-v", volume+":/runtime", meta["Id"], check=False)
            assert rejected.returncode != 0 and "refuses existing or unknown" in rejected.stderr, rejected.stderr
            assert before == volume_snapshot(volume), "rejected data was modified"
            events.append({"existingVolume": label, "unchanged": True, "exitCode": rejected.returncode})
        dc("stop", "cpa-manager-plus")
        dc("run", "--rm", "--no-deps", "--entrypoint", "sh", "cpa-manager-plus", "-ec",
           f"printf '\\n# unknown edit\\n' >> {runtime_root}/gateway/config.yaml")
        edited = dc("run", "--rm", "--no-deps", "--entrypoint", "sha256sum", "cpa-manager-plus",
                    f"{runtime_root}/gateway/config.yaml").stdout
        rejected = dc("run", "--rm", "--no-deps", "cpa-manager-plus", check=False)
        assert rejected.returncode != 0 and "modified or incompatible" in rejected.stderr, rejected.stderr
        assert edited == dc("run", "--rm", "--no-deps", "--entrypoint", "sha256sum", "cpamp-runtime" if False else "cpa-manager-plus",
                            f"{runtime_root}/gateway/config.yaml").stdout
        result.update(result="PASS", security="Ingress pending; CPA loopback-only; native no-key CPA is not independently secure",
                      notRun=["public admission", "real provider", "External/Upgrade migration", "Beta/Release qualification"])
    finally:
        cmd("docker", "rm", "-f", probe, check=False)
        dc("down", "--volumes", check=False)
        for volume in volumes:
            cmd("docker", "volume", "rm", volume, check=False)
        override.unlink(missing_ok=True)
        result["events"] = events
        (out/"result.json").write_text(json.dumps(result, indent=2)+"\n")
    print(json.dumps({k:v for k,v in result.items() if k != "events"}, indent=2))


if __name__ == "__main__":
    main()
