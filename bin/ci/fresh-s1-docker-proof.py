#!/usr/bin/env python3
"""Host-owned disposable S1 proof. Never targets an existing container/volume.

Requires locally built cpamp-s1-{runtime,manager,ingress}:local images.
Docker daemon/root and this serial launcher are the trusted host authority.
Only --ku selects the separately approved private revoke/necessary replacement
slice. It requires explicit CPAMP_PROOF_KEY_WRITE_APPROVED=plan64-private. No
clear, public Gateway, existing deployment or generation recovery is performed.
"""
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import pathlib
import secrets
import subprocess
import sys
import time
import threading

PIN = "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
ROOT = pathlib.Path(__file__).resolve().parents[2]


def command(*args, data=None):
    p = subprocess.run(args, input=data, text=True, capture_output=True, timeout=120)
    if p.returncode:
        # Do not echo command input, HTTP bodies, environment or container logs.
        raise RuntimeError("command failed: " + " ".join(args[:3]))
    return p.stdout.strip()


def namespace_owner(obj):
    # Docker accepts names, full IDs and unambiguous ID prefixes. Resolve them
    # through Docker instead of guessing which spelling inspect preserves.
    seen = set()
    while obj["HostConfig"]["NetworkMode"].startswith("container:"):
        assert obj["Id"] not in seen, "cyclic namespace reference; STOP"
        seen.add(obj["Id"])
        reference = obj["HostConfig"]["NetworkMode"].split(":", 1)[1]
        obj = json.loads(command("docker", "inspect", reference))[0]
    return obj["Id"]


def main():
    out = pathlib.Path(sys.argv[1]).resolve()
    out.mkdir(parents=True, exist_ok=False)
    name = "cpamp-s1-" + secrets.token_hex(6)
    containers, volumes, fixtures = [], [], []
    evidence = {"result": "STOP", "run": name, "events": []}
    source_paths = [
        "apps/manager-server/cmd/cpa-manager-plus/fresh.go",
        "apps/manager-server/cmd/cpa-manager-plus/fresh_keys.go",
        "apps/manager-server/cmd/cpa-manager-plus/fresh_key_install.go",
        "apps/manager-server/internal/repository/freshkeys/initial_install.go",
        "apps/manager-server/internal/service/freshkeys/service.go",
        "apps/manager-server/internal/service/cpa/private_initial_key.go",
        "apps/manager-server/internal/service/cpa/private_key_observation.go",
        "apps/web/src/fullweb/FreshKeyInstallation.tsx",
        "apps/web/src/fullweb/FreshKeyDetails.tsx",
        "apps/web/src/fullweb/freshKeyMetadata.ts",
        "bin/ci/fresh-s1-docker-proof.py",
    ] + [str(p.relative_to(ROOT)) for p in sorted((ROOT/"apps/manager-server/internal/service/freshkeyapply").glob("*.go")) if not p.name.endswith("_test.go")]
    evidence["sourceSHA256"]={p:hashlib.sha256((ROOT/p).read_bytes()).hexdigest() for p in source_paths}
    token, management = secrets.token_hex(32), secrets.token_hex(32)
    runtime, probe = name + "-runtime", name + "-probe"
    full = "--preflight" not in sys.argv[2:]
    ku = "--ku" in sys.argv[2:]
    h1 = "--h1" in sys.argv[2:]
    h1_host = None
    if h1:
        assert os.environ.get("CPAMP_PROOF_H1_WRITE_APPROVED") == "plan67-private", "dedicated disposable H1 write authorization missing"
        assert full and not ku and not any(v in sys.argv[2:] for v in ("--ui", "--s2b-offline", "--s2a-expiry", "--s2b-offline-expiry")), "H1 uses only the closed S1/S2A fixture"
        from fresh_h1_proof import Host
        h1_host = Host(command, http=None, name=name, runtime=runtime, probe=probe,
                       token=token, images=None, volumes=volumes, containers=containers, out=out)
    if ku:
        assert os.environ.get("CPAMP_PROOF_KEY_WRITE_APPROVED") == "plan64-private", "explicit private write approval missing"
    s2b = "--s2b-offline" in sys.argv[2:]
    s2a = "--s2a" in sys.argv[2:] or s2b or ku or h1
    expiry = "--s2a-expiry" in sys.argv[2:] or "--s2b-offline-expiry" in sys.argv[2:]
    if s2b:
        from fresh_s2b_proof import fixture
        provider_identity, provider_credential = fixture()
        evidence.update(liveProvider="LIVE NOT RUN / HOLD", controlledExternalEgress="NOT RUN / HOLD", fixture="OFFLINE TLS STUB ONLY")
    prefix = os.environ.get("CPAMP_PROOF_IMAGE_PREFIX", "cpamp-s1")
    tag = os.environ.get("CPAMP_PROOF_IMAGE_TAG", "local")
    images = {kind: prefix+"-"+kind+":"+tag for kind in ("runtime", "manager", "ingress")}
    if h1_host:
        h1_host.images = images
        h1_sources = ["bin/ci/fresh_h1_proof.py"]
        for folder in ("apps/runtime-supervisor/internal/privateconfig", "apps/manager-server/internal/service/cpa",
                       "apps/manager-server/internal/service/freshconfig", "apps/manager-server/internal/repository/freshkeys",
                       "apps/manager-server/internal/repository/freshstore"):
            h1_sources += [str(p.relative_to(ROOT)) for p in sorted((ROOT/folder).glob("*.go")) if not p.name.endswith("_test.go")]
        evidence["sourceSHA256"].update({p:hashlib.sha256((ROOT/p).read_bytes()).hexdigest() for p in h1_sources})
    if ku:
        p="apps/web/src/fullweb/carrier.css"
        evidence["sourceSHA256"][p] = hashlib.sha256((ROOT/p).read_bytes()).hexdigest()
        for p in ("bin/ci/fresh_ku_proof.py", "bin/ci/fresh_ku_browser.py", "bin/ci/fresh-ku-browser.mjs", "bin/ci/fresh-ku-network.mjs", "apps/manager-server/cmd/cpa-manager-plus/fresh_key_operation.go", "apps/manager-server/internal/repository/freshkeys/operation.go", "apps/manager-server/internal/service/cpa/private_key_operation.go", "apps/web/src/fullweb/FreshKeyRevocation.tsx", "apps/web/src/fullweb/FreshApiKeys.tsx"):
            evidence["sourceSHA256"][p] = hashlib.sha256((ROOT/p).read_bytes()).hexdigest()
        for locale in ("en", "zh-CN", "zh-TW", "ru"):
            p="apps/web/src/i18n/locales/"+locale+".json"
            evidence["sourceSHA256"][p] = hashlib.sha256((ROOT/p).read_bytes()).hexdigest()
    if s2a:
        from fresh_s2a_proof import source_paths as s2a_sources
        evidence["sourceSHA256"].update({p:hashlib.sha256((ROOT/p).read_bytes()).hexdigest() for p in s2a_sources(ROOT)})
    if s2b:
        for p in ("bin/ci/fresh_s2b_proof.py", "apps/manager-server/internal/service/cpa/private_provider_seed.go"):
            evidence["sourceSHA256"][p] = hashlib.sha256((ROOT/p).read_bytes()).hexdigest()

    def record(**fields):
        evidence["events"].append({"utcMs": int(time.time()*1000), **fields})

    def run(suffix, image, *options, args=()):
        container = name + "-" + suffix
        cid = command("docker", "run", "-d", "--name", container, "--platform", "linux/amd64",
                *options, image, *args)
        containers.append(container)
        if h1_host:
            h1_host.track(container, cid)
        return container

    def http(port, path, method="GET", body=None, key=None, scheme="Bearer"):
        program = '''import http.client,json,sys
v=json.load(sys.stdin)
c=http.client.HTTPConnection('127.0.0.1',v['port'],timeout=5)
h={'X-CPAMP-Runtime-Features':'active-gateway-artifact-v1'}
if v['key']: h['Authorization']=v['scheme']+' '+v['key']
if v['body'] is not None: h['Content-Type']='application/json'
c.request(v['method'],v['path'],json.dumps(v['body']) if v['body'] is not None else None,h)
r=c.getresponse(); data=r.read(1048577)
assert len(data)<=1048576
print(json.dumps({'status':r.status,'body':data.decode()}))
'''
        result = json.loads(command("docker", "exec", "-i", probe, "python3", "-c", program,
                                   data=json.dumps(dict(port=port, path=path, method=method, body=body, key=key, scheme=scheme))))
        record(port=port, path=path, method=method, status=result["status"])
        try:
            return result["status"], json.loads(result["body"])
        except ValueError:
            return result["status"], None

    try:
        if h1_host:
            h1_host.http = http
            for kind in images:
                image = json.loads(command("docker", "image", "inspect", images[kind]))[0]
                assert image["Os"] == "linux" and image["Architecture"] == "amd64"
                images[kind] = image["Id"]  # no tag drift during this operation window
            evidence["h1ImageIds"] = dict(images)
        metadata = json.loads(command("docker", "image", "inspect", images["runtime"]))[0]
        assert metadata["Os"] == "linux" and metadata["Architecture"] == "amd64"
        evidence["runtimeImageId"] = metadata["Id"]
        command("docker", "network", "create", "--internal", name)
        for suffix in ("runtime", "token", "manager", "permit") + (("h1-grant",) if h1 else ()):
            volume = name + "-" + suffix
            assert volume not in command("docker","volume","ls","-q").splitlines()
            command("docker", "volume", "create", volume)
            volumes.append(volume)
        # Newly created volumes: no existing deployment is ever selected.
        run("runtime", metadata["Id"], "--network", name,
            *(["-e", "CPAMP_PRIVATE_CONFIG_AUTHORITY=1", "-v", volumes[4]+":/run/cpamp-config:ro"] if h1 else []),
            *(["--add-host", "s2b-provider.invalid:127.0.0.1"] if s2b else []),
            "-e", "CPAMP_RUNTIME_ADDR=127.0.0.1:9081",
            "-e", "CPAMP_RUNTIME_IDENTITY=embedded-auth-proof",
            "-e", "CPAMP_RUNTIME_TOKEN=" + token,
            "-v", volumes[0]+":/runtime", "-v", volumes[1]+":/run/cpamp/runtime-secret")
        # Trusted tool shares only the network, with no credentials/data mounts.
        run("probe", "python:3.13-alpine", "--network", "container:"+runtime,
            *(["--tmpfs", "/tmp:rw,noexec,nosuid,size=4m"] if s2b else []),
            "--entrypoint", "sleep", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only", args=("1800",))
        for _ in range(40):
            try:
                code, status = http(9081, "/v1/runtime/status", key=token)
                if code == 200:
                    break
            except RuntimeError:
                pass
            time.sleep(0.2)
        assert status["state"] == "offline"
        evidence["initialStatus"] = status
        assert command("docker", "exec", runtime, "sha256sum", "/usr/local/bin/cli-proxy-api").split()[0] == PIN
        # No child is running. Seed ONLY the task-created empty config, once.
        seed = (ROOT / "docker/runtime/config.v8.loopback-proof.yaml").read_text()
        seed = seed.replace("secret-key: ''", "secret-key: '"+management+"'")
        if h1_host:
            seed = h1_host.prehash(seed, management)
        if full and not s2b:
            seed += "\napi-keys:\n  openai-compatibility:\n    - name: s1-synthetic\n      base-url: http://127.0.0.1:18321/v1\n      keys:\n        - api-key: s1-synthetic-upstream-only\n      models:\n        - name: s1-proof-model\n          alias: s1-proof-model\n"
        if s2b:
            from fresh_s2b_proof import seed_config, start_offline_tls
            seed = seed_config(management, provider_identity, provider_credential)
            start_offline_tls(command, runtime, probe, provider_credential)
        command("docker", "exec", "-i", runtime, "sh", "-ec",
                "test -z \"$(ls -A /runtime/gateway/auth)\"; cat > /runtime/gateway/config.yaml; chown 10001:10001 /runtime/gateway/config.yaml; chmod 600 /runtime/gateway/config.yaml", data=seed)
        if h1_host:
            h1_host.provision(seed)
        if full:
            if s2a:
                from fresh_s2a_proof import start_meter
                start_meter(run, runtime)
            manager_env = ["-e", "HTTP_ADDR=127.0.0.1:"+("18320" if s2a else "18319"), "-e", "USAGE_DB_PATH=/data/usage.sqlite",
                "-e", "CPA_MANAGER_DATA_KEY_PATH=/data/data.key", "-e", "CPA_UPSTREAM_URL=http://127.0.0.1:8317",
                "-e", "CPAMP_RUNTIME_URL=http://127.0.0.1:9081", "-e", "CPAMP_RUNTIME_TOKEN_FILE=/run/cpamp/runtime-secret/token",
                "-e", "CPAMP_INGRESS_CONTROL_URL=http://127.0.0.1:9082", "-e", "CPAMP_PUBLIC_ORIGIN=http://127.0.0.1:28471",
                "-e", "CPAMP_UPDATE_CHECK_ENABLED=false", "-e", "CPAMP_PRIVATE_S1_PERMIT=/run/cpamp-s1/permit.json"]
            if ku:
                manager_env += ["-e", "CPAMP_PRIVATE_KEY_OPERATIONS=1"]
            if h1:
                manager_env += ["-e", "CPAMP_PRIVATE_KEY_OPERATIONS=1", "-e", "CPAMP_PRIVATE_CONFIG_AUTHORITY=1", "-e", "CPAMP_PRIVATE_H1=1", "-v", volumes[4]+":/run/cpamp-config:ro"]
            if s2a:
                from fresh_s2a_proof import start_relay
                start_relay(command, probe, replay_test=s2b)
                manager_env += ["-e", ("CPAMP_PRIVATE_S2B_CHAT=1" if s2b else "CPAMP_PRIVATE_S2A_CHAT=1")]
            init = command("docker", "run", "--rm", "--network", "none", *manager_env,
                "-v", volumes[2]+":/data", images["manager"], "initialize")
            payload = init.split("#handoff=")[1].strip()
            handoff = json.loads(base64.urlsafe_b64decode(payload + "="*(-len(payload)%4)))
            run("manager", images["manager"], "--network", "container:"+runtime, *manager_env,
                "-v", volumes[2]+":/data", "-v", volumes[1]+":/run/cpamp/runtime-secret:ro", "-v", volumes[3]+":/run/cpamp-s1:ro")
            run("ingress", images["ingress"], "--network", "container:"+runtime,
                "-e", "CPAMP_INGRESS_ADDR=0.0.0.0:28471", "-e", "CPAMP_MANAGER_URL=http://127.0.0.1:18319",
                "-e", "CPAMP_GATEWAY_URL=http://127.0.0.1:8317", "-e", "CPAMP_INGRESS_CONTROL_ADDR=127.0.0.1:9082",
                "-e", "CPAMP_PRIVATE_S2A_CHAT="+("1" if s2a and not s2b else "0"),
                "-e", "CPAMP_PRIVATE_S2B_CHAT="+("1" if s2b else "0"),
                "-e", "CPAMP_PRIVATE_S2B_MODEL="+(provider_identity["model"] if s2b else ""))
            bootstrap = "/.well-known/cpamp-bootstrap/api/v2/"
            for _ in range(40):
                try:
                    if http(28471,"/.well-known/cpamp-bootstrap")[0] == 200: break
                except RuntimeError: pass
                time.sleep(0.2)
            code, session = http(28471,bootstrap+"claim/exchange","POST",{k:handoff[k] for k in ("identity","claimGeneration","token")})
            assert code==200
            session_token=session["token"]
            def fresh(resource,body):
                code,value=http(28471,bootstrap+resource,"POST",body,session_token,"Fresh")
                assert code==200,(resource,code)
                return value
            admin=fresh("admin/generate",{})["key"]
            fresh("admin/commit",{"identity":handoff["identity"],"commitId":secrets.token_hex(16),"key":admin})
            fresh("routing/prepare",{"identity":handoff["identity"],"target":"/control"})
            operation=fresh("routing/activate",{"identity":handoff["identity"]})
            api="/control/api/v2/"
            strategy_target={"expectedRuntimeIdentity":status["runtimeIdentity"],"expectedRuntimeGeneration":status["runtimeGeneration"],"expectedArtifactId":"sha256:"+PIN}
            for _ in range(4):
                code,operation=http(28471,api+"strategy","POST",{"identity":operation["identity"],"expectedRevision":operation["revision"],"strategy":"new_managed",**strategy_target},admin)
                assert code==200,("strategy",code)
                if operation["strategy"]["stage"]=="committed":break
            assert operation["strategy"]["stage"]=="committed"
        else:
            code, started = http(9081, "/v1/runtime/operations/start", "POST", {
                "operationId": name+"-start", "expectedRuntimeIdentity": status["runtimeIdentity"],
                "expectedRuntimeGeneration": status["runtimeGeneration"], "expectedActiveArtifactId": "sha256:"+PIN}, token)
            assert code == 200 and started["state"] == "succeeded"
        for _ in range(40):
            code, status = http(9081, "/v1/runtime/status", key=token)
            if status["state"] == "ready":
                break
            time.sleep(0.2)
        assert status["state"] == "ready" and status["activeGatewayArtifact"]["artifactId"] == "sha256:"+PIN
        evidence["liveStatus"] = status
        assert http(8317, "/v8/management/config/access/api-keys", key=management) == (200, [])
        assert http(8317, "/v8/management/config/access/api-keys", key="wrong")[0] == 401
        if full:
            code,projection=http(8317,"/v8/management/config",key=management)
            assert code==200
            # Record structure and synthetic fixtures only. Management secrets
            # are redacted before any evidence is written.
            evidence["managementProjectionFields"]=sorted(projection.get("management",{}))
            projection.get("management",{}).pop("secret-key",None)
            if not s2b:
                evidence["configProjectionWithoutManagementSecret"]=projection
            else:
                evidence["providerProjectionMatchesHostSeed"] = projection["api-keys"]["openai-compatibility"][0]["base-url"] == provider_identity["target"]
        inspect = json.loads(command("docker", "inspect", runtime))[0]
        assert not inspect["HostConfig"]["PortBindings"]
        ip = inspect["NetworkSettings"]["Networks"][name]["IPAddress"]
        # Independent untrusted bridge peer, with a reachable-loopback control.
        command("docker", "run", "--rm", "--network", name, "python:3.13-alpine", "python3", "-c",
                "import socket; ip='"+ip+"'; "
                "\nfor port in (8317,9081):\n s=socket.socket(); s.settimeout(2); assert s.connect_ex((ip,port)) != 0; s.close()")
        record(preflight="PASS", privatePortsRefused=[8317,9081], emptyKeys=True, uniqueWriter="host-owned serial launcher")
        evidence["result"] = "PREFLIGHT_PASS_NO_CLIENT_KEY_WRITE"
        if not full:
            return
        # Real, synthetic-only upstream with an in-memory arrival counter. It has
        # no runtime token, Management key, host mount or external egress.
        upstream = '''import http.server,json
events=[]
class H(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def do_GET(self):
  b=json.dumps(events).encode(); self.send_response(200); self.end_headers(); self.wfile.write(b)
 def do_POST(self):
  value=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  assert self.headers.get('Authorization')=='Bearer s1-synthetic-upstream-only'
  events.append({'path':self.path,'model':value['model']})
  b=json.dumps({'id':'s1-synthetic','object':'chat.completion','model':'s1-proof-model','choices':[{'index':0,'message':{'role':'assistant','content':'synthetic S1 response'},'finish_reason':'stop'}],'usage':{'prompt_tokens':1,'completion_tokens':1,'total_tokens':2}}).encode()
  self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(b))); self.end_headers(); self.wfile.write(b)
http.server.ThreadingHTTPServer(('127.0.0.1',18321),H).serve_forever()
'''
        if not s2b:
            command("docker","exec","-d",probe,"python3","-c",upstream)
        time.sleep(0.3)
        def chat(port,key,expected,arrivals,stream=False):
            before=len(http(18321,"/events")[1])
            if s2a:
                from fresh_s2a_proof import cpa_chat_count
                cpa_before=cpa_chat_count(command,runtime)
            code,_=http(port,"/v1/chat/completions","POST",{"model":"s1-proof-model","stream":stream,"messages":[{"role":"user","content":"disposable S1 proof"}]},key)
            after=len(http(18321,"/events")[1])
            if s2a:
                cpa_arrivals=cpa_chat_count(command,runtime)-cpa_before
                record(scenario="s1-pre-post-install",edge="private-cpa" if port==8317 else "ingress",status=code,cpaChatArrivals=cpa_arrivals,upstreamArrivals=after-before)
                assert cpa_arrivals==(1 if port==8317 else arrivals),("CPA arrivals",cpa_arrivals)
            record(edge="private-cpa" if port==8317 else "ingress",credential="absent" if key is None else "wrong" if key=="wrong" else "valid",stream=stream,status=code,upstreamArrivals=after-before)
            assert code==expected and after-before==arrivals,(code,after-before)
        if not s2b:
            for key in (None,"wrong"):
                chat(8317,key,200,1)  # Empty official CPA access list permits both.
        code,created=http(28471,api+"api-keys","POST",{"requestId":secrets.token_hex(16)},admin)
        assert code==201
        secret=created["createdSecret"]; key_id=created["key"]["id"]
        if not s2b:
            chat(8317,secret,200,1)
            for key in (None,"wrong",secret):
                chat(28471,key,503,0)
                chat(28471,key,503,0,stream=True)
        else:
            from fresh_s2b_proof import check
            for label,key in (("zero-CPA-keys-absent",None),("zero-CPA-keys-wrong","wrong"),("canonical-staged-not-installed",secret)):
                check(command,http,record,runtime,label,key)
        install_path=api+"api-keys/"+key_id+"/initial-install"
        for credential in (None,"wrong"):
            assert http(28471,install_path,"POST",{},credential)[0]==401
        # Before trusted host evidence is mounted, even Admin cannot install.
        assert http(28471,install_path,"POST",{},admin)[0]==409
        assert http(28471,install_path,"POST",{"sourceExclusive":True,"runtimeGeneration":status["runtimeGeneration"]},admin)[0]==400
        # Verify mounts/namespace admission from Docker, never from an API flag.
        if "--untrusted-name-join" in sys.argv[2:]:
            fixture = name+"-untrusted-name-join"
            command("docker","run","-d","--name",fixture,"--network","container:"+runtime,
                    "--cap-drop=ALL","--security-opt","no-new-privileges","--read-only",
                    "python:3.13-alpine","sleep","1800")
            fixtures.append(fixture)  # Cleanup-owned, never a trusted consumer.
        all_containers=json.loads(command("docker","inspect",*containers))
        users=command("docker","ps","-aq").splitlines()
        consumers=[]
        for obj in json.loads(command("docker","inspect",*users)):
            owned=obj["Name"].lstrip("/") in containers
            mounts={m["Name"] for m in obj.get("Mounts",[]) if m["Type"]=="volume"}
            if mounts.intersection(volumes): assert owned,"untrusted volume consumer"
            if namespace_owner(obj)==inspect["Id"]:
                consumers.append(dict(container=obj["Name"].lstrip("/"),networkMode=obj["HostConfig"]["NetworkMode"],resolvedRuntimeId=inspect["Id"],trusted=owned))
        evidence["runtimeNamespaceConsumers"]=consumers
        untrusted=[obj for obj in consumers if not obj["trusted"]]
        if untrusted:
            # This branch precedes permit creation and every Client Key PUT.
            assert http(28471,install_path,"POST",{},admin)[0]==409
            assert http(8317,"/v8/management/config/access/api-keys",key=management)==(200,[])
            assert command("docker","exec",name+"-manager","sh","-ec","test ! -e /run/cpamp-s1/permit.json; echo absent")=="absent"
            record(namespaceGate="STOP",permitIssued=False,clientKeyPUTAttempted=False,emptyCPAKeys=True)
            raise RuntimeError("untrusted runtime namespace consumer; STOP before permit/PUT")
        for obj in all_containers:
            assert not obj["HostConfig"]["Privileged"] and not obj["HostConfig"]["PortBindings"]
        evidence["containerImages"]={obj["Name"].lstrip("/"):obj["Image"] for obj in all_containers}
        assert json.loads(command("docker","network","inspect",name))[0]["Internal"]
        manager_inspect=next(o for o in all_containers if o["Name"]=="/"+name+"-manager")
        assert all(not m["RW"] for m in manager_inspect["Mounts"] if m["Name"] in (volumes[1],volumes[3]))
        # Live process executable hash, UID and command, independently from manifest.
        process=command("docker","exec","--user","10001:10001",runtime,"sh","-ec",'''for p in /proc/[0-9]*; do
 [ -r "$p/comm" ] || continue
 [ "$(cat "$p/comm")" = cli-proxy-api ] || continue
 sha256sum "$p/exe"; readlink "$p/cwd"; sed -n '/^Uid:/p; /^Gid:/p' "$p/status"; exit 0
done
exit 1''')
        assert process.split()[0]==PIN and "Uid:\t10001\t10001\t10001\t10001" in process and "Gid:\t10001\t10001\t10001\t10001" in process
        evidence["process"]=process
        command("docker","run","--rm","--network",name,"python:3.13-alpine","python3","-c",
            "import socket,http.client; ip='"+ip+"'\nfor port in (8317,9081,18319,9082,18321):\n s=socket.socket(); s.settimeout(2); assert s.connect_ex((ip,port))!=0; s.close()\nc=http.client.HTTPConnection(ip,28471,timeout=3); c.request('GET','/v1/models'); assert c.getresponse().status==503")
        permit=dict(runId=name,generation=str(status["runtimeGeneration"]),managementKey=management,issuedAtMs=int(time.time()*1000),expiresAtMs=int(time.time()*1000)+(60000 if expiry else 180000))
        if s2b:
            permit["s2b"] = provider_identity
        command("docker","run","--rm","-i","--network","none","--entrypoint","sh","-v",volumes[3]+":/permit",images["manager"],"-ec","umask 077; cat > /permit/permit.json; chmod 600 /permit/permit.json",data=json.dumps(permit))
        record(hostAuthority="PASS",containers=containers,volumes=volumes,networkInternal=True,permitMountedReadOnly=True,managementCredential="unique ephemeral secret; CPA hash + Manager read-only permit only")
        code,state=http(28471,install_path,key=admin)
        assert code==200 and state["installAvailable"] and not state["attempted"]
        if "--ui" in sys.argv[2:]:
            # A host-loopback test relay reaches only Ingress inside the egress-
            # free namespace. No CPA/control port is published. The synthetic
            # Admin handoff exists in memory for this local browser test only.
            class Relay(BaseHTTPRequestHandler):
                def log_message(self,*args): pass
                def do_GET(self): self.forward()
                def do_POST(self): self.forward()
                def do_PUT(self): self.forward()
                def forward(self):
                    if self.path=="/__s1-test-admin":
                        data=json.dumps({"admin":admin,"keyId":key_id}).encode()
                        self.send_response(200);self.send_header("Cache-Control","no-store");self.send_header("Content-Type","application/json");self.end_headers();self.wfile.write(data);return
                    body=self.rfile.read(int(self.headers.get("Content-Length",0)))
                    program='''import base64,http.client,json,sys
v=json.load(sys.stdin);c=http.client.HTTPConnection('127.0.0.1',28471,timeout=10)
c.request(v['method'],v['path'],base64.b64decode(v['body']),v['headers'])
r=c.getresponse();print(json.dumps({'status':r.status,'headers':r.getheaders(),'body':base64.b64encode(r.read()).decode()}))'''
                    try:
                        value=json.loads(command("docker","exec","-i",probe,"python3","-c",program,data=json.dumps({"method":self.command,"path":self.path,"headers":{k:v for k,v in self.headers.items() if k.lower() not in ("host","connection","content-length")},"body":base64.b64encode(body).decode()})))
                        data=base64.b64decode(value["body"])
                        self.send_response(value["status"])
                        for k,v in value["headers"]:
                            if k.lower() not in ("connection","transfer-encoding","content-length"):
                                self.send_header(k,v)
                        self.send_header("Content-Length",str(len(data)));self.end_headers();self.wfile.write(data)
                    except Exception:
                        self.send_response(502);self.end_headers()
            relay=ThreadingHTTPServer(("127.0.0.1",0),Relay)
            threading.Thread(target=relay.serve_forever,daemon=True).start()
            (out/"ui.json").write_text(json.dumps({"url":"http://127.0.0.1:"+str(relay.server_port)+"/control","run":name}))
            print("UI_READY "+str(out/"ui.json"),flush=True)
            deadline=time.monotonic()+120
            while not (out/"ui-done").exists() and time.monotonic()<deadline: time.sleep(0.25)
            relay.shutdown();relay.server_close()
            assert (out/"ui-done").exists(),"browser test timed out; STOP"
            code,state=http(28471,install_path,key=admin)
        else:
            code,state=http(28471,install_path,"POST",{},admin)
        assert code==200,("install",code,state)
        assert state["cpaConfigured"] and not state["ingressAuthorized"]
        evidence["installObservation"]=state
        assert http(28471,install_path,"POST",{},admin)[0]==409
        # Allow only READ sampling while the CPA watcher applies its first Key.
        for _ in range(20):
            if http(8317,"/v1/models")[0]==401:break
            time.sleep(0.25)
        if not s2b:
            for key,expected,arrivals in ((None,401,0),("wrong",401,0),(secret,200,1)):
                chat(8317,key,expected,arrivals)
                chat(28471,key,200 if s2a and key==secret else 503,1 if s2a and key==secret else 0)
                chat(28471,key,503,0,stream=True)
        code,state=http(28471,install_path,key=admin)
        assert code==200 and state["runtimeState"]=="effective_observed"
        evidence["effectiveObservation"]=state
        if h1_host:
            h1_host.exercise(admin, api, permit, secret, key_id, evidence)
            evidence["result"] = "PASS_DISPOSABLE_H1_COMBINED_NATIVE_LINUX_BETA_NOT_PROVEN"
            return
        if ku:
            from fresh_ku_proof import exercise
            exercise(command,http,record,evidence,name,probe,runtime,secret,key_id,admin,api,permit,volumes,containers,images,namespace_owner,out)
            evidence["result"]="PASS_PRIVATE_KEY_REVOKE_REPLACE_OFFLINE_LIVE_HOLD"
            return
        if s2b:
            from fresh_s2b_proof import exercise as exercise_s2b
            exercise_s2b(command,http,record,evidence,runtime,probe,name,secret,admin,api,expiry,permit,token)
            if expiry:
                evidence["result"] = "PASS_OFFLINE_S2B_EXPIRY_ONLY_LIVE_HOLD"
                return
        elif s2a:
            from fresh_s2a_proof import exercise
            exercise(command, http, record, evidence, name, probe, runtime, ip, token, secret, admin, api, permit, expiry)
            if expiry:
                evidence["result"]="PASS_PRIVATE_S2A_EXPIRED_PERMIT_DENIED"
                return
        # Read actual CPA disk state without storing or logging any key bytes.
        disk=command("docker","exec","--user","10001:10001",runtime,"cat","/runtime/gateway/config.yaml")
        assert disk.count(secret)==1
        evidence["disk"]=dict(singleCanonicalKeyPresent=True,sha256=hashlib.sha256(disk.encode()).hexdigest())
        storage_check='''import json,sqlite3,sys
v=json.load(sys.stdin);db=sqlite3.connect('file:/data/usage.sqlite?mode=ro',uri=True)
rows=db.execute('select api_key_id,secret_ciphertext from fresh_client_api_keys').fetchall()
assert len(rows)==v['expectedRows']
selected=[row for row in rows if row[0]==v['id']]
assert len(selected)==1 and v['secret'] not in selected[0][1] and len(selected[0][1])>len(v['secret'])
assert db.execute('select count(*) from gateway_api_key_identities where id=?',(v['id'],)).fetchone()[0]==1
journal=db.execute("select value from settings where key='fresh_s1_initial_key_install_v1'").fetchone()[0]
assert v['secret'] not in journal and json.loads(journal)['keyId']==v['id']
'''
        command("docker","run","--rm","-i","--network","none","-v",volumes[2]+":/data:ro","python:3.13-alpine","python3","-c",storage_check,data=json.dumps({"id":key_id,"secret":secret,"expectedRows":2 if s2a else 1}))
        record(managerDisk="canonical identity + encrypted staged secret + nonsecret mutation journal verified read-only")
        command("docker","restart",name+"-manager")
        for _ in range(40):
            try:
                code,reloaded=http(28471,install_path,key=admin)
                if code==200:break
            except RuntimeError:pass
            time.sleep(0.25)
        assert code==200 and reloaded["attempted"] and not reloaded["installAvailable"]
        assert http(28471,install_path,"POST",{},admin)[0]==409
        assert http(8317,"/v8/management/config/access/api-keys",key=management)==(200,[secret])
        # Restart changes generation. No attempt to heal/bypass or start CPA.
        command("docker","restart",runtime)
        # Explicitly reattach trusted namespace consumers; this is test setup,
        # not automatic recovery or a bypass of the stored Fresh generation.
        for container in ((name+"-meter",) if s2a else ())+(probe,name+"-manager",name+"-ingress"):
            command("docker","restart",container)
        if s2a:
            start_relay(command, probe, replay_test=s2b)
        time.sleep(1)
        code,new_status=http(9081,"/v1/runtime/status",key=token)
        assert code==200 and new_status["runtimeGeneration"]!=status["runtimeGeneration"] and new_status["state"]=="offline"
        evidence["afterRestartStatus"]=new_status
        # Restore the original control route before testing its generation
        # fence. A transient Ingress 503 is not evidence of an Admin 409.
        for _ in range(40):
            try:
                if http(28471,api+"operation",key=admin)[0]==200:break
            except RuntimeError:pass
            time.sleep(0.25)
        assert http(28471,install_path,"POST",{},admin)[0]==409
        assert http(28471,install_path,key=admin)[0]==409
        # The existing canonical strategy must keep its mismatch 409 as well.
        # Extensions may persist Config intent via the SAME Fresh CAS. Refresh
        # that product revision so this negative still reaches the generation
        # fence; separately retain the stale-checkpoint rejection.
        code,current_operation=http(28471,api+"operation",key=admin)
        assert code==200
        if current_operation["revision"]!=operation["revision"]:
            assert http(28471,api+"strategy","POST",{"identity":operation["identity"],"expectedRevision":operation["revision"],"strategy":"new_managed",**strategy_target},admin)[0]==400
        assert http(28471,api+"strategy","POST",{"identity":operation["identity"],"expectedRevision":current_operation["revision"],"strategy":"new_managed",**strategy_target},admin)[0]==409
        assert http(28471,"/v1/models")[0]==503
        if s2b:
            from fresh_s2a_proof import counts
            from fresh_s2b_proof import BODY
            before = counts(command,runtime)
            assert http(28471,"/v1/chat/completions","POST",BODY,secret)[0] == 503
            assert counts(command,runtime)["cpaChat"] == before["cpaChat"]
            record(scenario="changed-generation-CPA-offline",status=503,cpaChatArrivals=0)
        elif s2a:
            from fresh_s2a_proof import check_generation_denial
            check_generation_denial(command,http,record,runtime,secret)
        evidence["result"]="PASS_OFFLINE_S2B_ONLY_LIVE_HOLD" if s2b else "PASS_PRIVATE_S2A_CHAT" if s2a else "PASS_PRIVATE_S1_GATEWAY_STOP"
    except Exception:
        evidence["result"]="STOP"
        raise
    finally:
        if h1_host:
            # Preserve receipts, journals and forensic state even on failure.
            # Never let ordinary S1 cleanup delete an unconsumed H1 receipt.
            try:
                h1_host.quiesce()
            except Exception:
                evidence["result"] = "STOP"
                evidence["h1Shutdown"] = "UNPROVEN / NO RECOVERY OR HANDOFF"
                raise
            finally:
                (out / "evidence.json").write_text(json.dumps(evidence, indent=2)+"\n")
        else:
            (out / "evidence.json").write_text(json.dumps(evidence, indent=2)+"\n")
            for container in reversed(containers+fixtures):
                command("docker", "rm", "-f", container)
            for volume in volumes:
                command("docker", "volume", "rm", volume)
            command("docker", "network", "rm", name)


if __name__ == "__main__":
    main()
