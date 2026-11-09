#!/usr/bin/env python3
"""Verify slim runtime CPA acquisition and Fresh New end-to-end through public Ingress."""

import base64
import io
import json
import os
import re
import secrets
import socket
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.request
from pathlib import Path

VERSION = "8.0.55"
PINS = {
    "amd64": "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7",
    "arm64": "4a5b43cc3700d36c72b7c1953e9b11eca0888c13b789ea2f99471117504a7d5d",
}
REDACT_PATTERNS = [
    re.compile(r"(Authorization:\s*(?:Bearer|Fresh)\s+)[^\s\"']+", re.I),
    re.compile(r"(X-Management-Key:\s*)[^\s\"']+", re.I),
    re.compile(r"([\"']?(?:secret-key|CPAMP_RUNTIME_TOKEN|managementKey|token|key)[\"']?\s*[:=]\s*[\"']?)[^\s\"',}]+", re.I),
]


def run(args, check=True, env=None):
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    proc = subprocess.run(args, text=True, capture_output=True, env=merged_env)
    if check and proc.returncode != 0:
        raise RuntimeError(f"{' '.join(args)} failed ({proc.returncode}): {proc.stderr or proc.stdout}")
    return (proc.stdout or "") + (proc.stderr or "")


def redact(value, secret_values):
    if isinstance(value, dict):
        return {k: redact(v, secret_values) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v, secret_values) for v in value]
    if isinstance(value, str):
        out = value
        for secret in secret_values:
            if secret:
                out = out.replace(secret, "[REDACTED]")
        for pattern in REDACT_PATTERNS:
            out = pattern.sub(r"\1[REDACTED]", out)
        return out
    return value


def pick_smoke_port():
    configured = os.environ.get("CPAMP_PUBLIC_PORT", "").strip()
    if configured:
        return configured
    for port in range(38400, 38450):
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                sock.bind(("127.0.0.1", port))
                return f"127.0.0.1:{port}"
            except OSError:
                continue
    return "127.0.0.1:0"


def build_tampered_archive():
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for name, mode, data in (
            ("cli-proxy-api", 0o755, b"#!/bin/sh\necho tampered-cpa-binary\n"),
            ("LICENSE", 0o644, b"MIT License\nTampered test fixture\n"),
        ):
            info = tarfile.TarInfo(name=name)
            info.size = len(data)
            info.mode = mode
            info.type = tarfile.REGTYPE
            tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def public_http(origin, path, method="GET", body=None, auth_scheme=None, auth_token=None, content_type=None, timeout=60):
    headers = {}
    data = None
    if auth_scheme and auth_token:
        headers["Authorization"] = f"{auth_scheme} {auth_token}"
    if body is not None:
        if isinstance(body, (bytes, bytearray)):
            data = bytes(body)
            headers["Content-Type"] = content_type or "application/gzip"
        else:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = content_type or "application/json"
    req = urllib.request.Request(origin + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            return resp.status, (json.loads(raw) if raw.strip().startswith(("{", "[")) else raw)
    except urllib.error.HTTPError as err:
        raw = err.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(raw) if raw.strip().startswith(("{", "[")) else raw
        except json.JSONDecodeError:
            parsed = raw
        return err.code, parsed


def decode_handoff(init_output):
    for line in init_output.splitlines():
        if "#handoff=" in line:
            encoded = line.split("#handoff=", 1)[1].strip()
            padded = encoded + "=" * (-len(encoded) % 4)
            return json.loads(base64.urlsafe_b64decode(padded).decode("utf-8"))
    raise RuntimeError(f"initialize output did not contain #handoff=: {init_output}")


def inspect_state(cid):
    meta = json.loads(run(["docker", "inspect", cid]))[0]["State"]
    return meta.get("Status"), (meta.get("Health") or {}).get("Status")


def wait_until(predicate, description, timeout=60, interval=0.25):
    deadline = time.time() + timeout
    last_err = None
    while time.time() < deadline:
        try:
            value = predicate()
            if value:
                return value
        except Exception as err:  # noqa: BLE001
            last_err = err
        time.sleep(interval)
    raise RuntimeError(f"timed out waiting for {description}: {last_err}")


def claim_fresh_session(dc, origin, secret_values):
    wait_until(
        lambda: public_http(origin, "/.well-known/cpamp-bootstrap")[0] == 200,
        "public Ingress bootstrap discovery",
        timeout=60,
    )
    init_out = dc(
        "exec",
        "-T",
        "--user",
        "10002:10002",
        "cpa-manager-plus",
        "/usr/local/bin/cpa-manager-plus",
        "initialize",
    )
    handoff = decode_handoff(init_out)
    secret_values.append(handoff.get("token", ""))
    def try_exchange():
        code, body = public_http(
            origin,
            "/.well-known/cpamp-bootstrap/api/v2/claim/exchange",
            method="POST",
            body={k: handoff[k] for k in ("identity", "claimGeneration", "token")},
        )
        if code == 404:
            return None
        assert code == 200 and isinstance(body, dict) and body.get("token"), (code, body)
        return body

    exchanged = wait_until(try_exchange, "Fresh claim exchange after initialize", timeout=30)
    session_token = exchanged["token"]
    secret_values.append(session_token)
    return handoff, session_token, exchanged.get("operation")


def inspect_cpa_process(dc):
    raw = dc(
        "exec",
        "-T",
        "--user",
        "10001:10001",
        "cpa-manager-plus",
        "sh",
        "-eu",
        "-c",
        'for p in /proc/[0-9]*; do '
        'if [ "$(cat "$p/comm" 2>/dev/null || true)" = "cli-proxy-api" ]; then '
        'echo "PID=${p#/proc/}"; '
        'echo "EXE=$(readlink "$p/exe")"; '
        'echo "SHA=$(sha256sum "$p/exe" | awk "{print \\$1}")"; '
        'grep -E "^(Uid|Gid):" "$p/status"; '
        "exit 0; "
        "fi; "
        "done; "
        "exit 1",
    )
    return dict(
        line.split("=", 1) if "=" in line else [line.split(":", 1)[0], line]
        for line in raw.strip().splitlines()
    )


def main(argv):
    if len(argv) != 3:
        raise SystemExit("usage: runtime-slim-acquire-smoke.py IMAGE_SLIM IMAGE_FULL NEW_OUT")
    slim_image, full_image, output = argv
    out = Path(output).resolve()
    out.mkdir(parents=True, exist_ok=False)

    repo_root = Path(__file__).resolve().parents[2]
    compose_file = repo_root / "docker-compose.yml"
    slim_meta = json.loads(run(["docker", "image", "inspect", slim_image]))[0]
    full_meta = json.loads(run(["docker", "image", "inspect", full_image]))[0]
    arch = slim_meta["Architecture"]
    assert slim_meta["Os"] == "linux" and arch in PINS, (slim_meta["Os"], arch)
    assert full_meta["Os"] == "linux" and full_meta["Architecture"] == arch, (full_meta["Os"], full_meta["Architecture"])
    pin = PINS[arch]
    expected_artifact_id = "sha256:" + pin

    base_project = os.environ.get("CPAMP_SMOKE_PROJECT") or f"s2a-slim-acquire-{secrets.token_hex(4)}"
    slim_project = f"{base_project}-slim"
    full_project = f"{base_project}-full"
    slim_port = pick_smoke_port()
    secret_values = []
    evidence = {
        "slimImage": slim_image,
        "fullImage": full_image,
        "architecture": arch,
        "expectedVersion": VERSION,
        "expectedArtifactId": expected_artifact_id,
    }

    def make_dc(project_name, image_meta, public_port, label):
        override_file = out / f"compose.{label}.override.json"
        override_file.write_text(
            json.dumps(
                {
                    "services": {
                        "cpa-manager-plus": {
                            "image": image_meta["Id"],
                            "platform": "linux/" + arch,
                            "environment": {
                                "CPAMP_PUBLIC_ORIGIN": "http://127.0.0.1:18317",
                            },
                        }
                    }
                }
            ),
            encoding="utf-8",
        )
        compose_env = {
            "CPAMP_PUBLIC_PORT": public_port,
        }

        def dc(*args, check=True):
            return run(
                [
                    "docker",
                    "compose",
                    "--project-directory",
                    str(repo_root),
                    "-p",
                    project_name,
                    "-f",
                    str(compose_file),
                    "-f",
                    str(override_file),
                    *args,
                ],
                check=check,
                env=compose_env,
            )

        return dc, override_file

    slim_dc, slim_override = make_dc(slim_project, slim_meta, slim_port, "slim")
    full_dc = None
    full_override = None
    try:
        slim_dc("up", "-d", "--no-build", "--pull", "never")
        slim_cid = slim_dc("ps", "-q", "cpa-manager-plus").strip()
        assert slim_cid, "slim cpa-manager-plus container ID is empty"
        slim_origin = "http://" + slim_dc("port", "cpa-manager-plus", "18317").strip().splitlines()[-1]

        image_manifest = json.loads(
            slim_dc("exec", "-T", "cpa-manager-plus", "cat", "/usr/local/share/cpamp/runtime/cpa-artifact.json")
        )
        assert image_manifest.get("version") == VERSION and image_manifest.get("artifactId") == expected_artifact_id, image_manifest
        evidence["imageManifest"] = image_manifest

        handoff, session_token, _ = claim_fresh_session(slim_dc, slim_origin, secret_values)
        bootstrap_api = "/.well-known/cpamp-bootstrap/api/v2/"

        # Step (a): slim, fresh volume -> required=true, active=null, pinned equals image manifest.
        unauth_code, _ = public_http(slim_origin, bootstrap_api + "runtime/artifact")
        assert unauth_code == 401, unauth_code
        code, initial_artifact = public_http(
            slim_origin,
            bootstrap_api + "runtime/artifact",
            auth_scheme="Fresh",
            auth_token=session_token,
        )
        assert code == 200 and isinstance(initial_artifact, dict), (code, initial_artifact)
        assert initial_artifact.get("required") is True, initial_artifact
        assert initial_artifact.get("active") is None, initial_artifact
        assert initial_artifact.get("operation") is None, initial_artifact
        assert initial_artifact.get("pinned") == {
            "version": image_manifest["version"],
            "artifactId": image_manifest["artifactId"],
        }, initial_artifact
        evidence["stepA_slimInitialArtifact"] = initial_artifact

        # Step (b): upload a tampered archive -> 422 artifact_mismatch; still required=true; no selection.json or stage dir.
        tampered_bytes = build_tampered_archive()
        tampered_code, tampered_resp = public_http(
            slim_origin,
            bootstrap_api + "runtime/artifact/upload",
            method="PUT",
            body=tampered_bytes,
            auth_scheme="Fresh",
            auth_token=session_token,
            content_type="application/gzip",
        )
        assert tampered_code == 422 and isinstance(tampered_resp, dict) and tampered_resp.get("code") == "artifact_mismatch", (
            tampered_code,
            tampered_resp,
        )
        code, after_tampered = public_http(
            slim_origin,
            bootstrap_api + "runtime/artifact",
            auth_scheme="Fresh",
            auth_token=session_token,
        )
        assert code == 200 and after_tampered.get("required") is True and after_tampered.get("active") is None, (
            code,
            after_tampered,
        )
        slim_dc(
            "exec",
            "-T",
            "cpa-manager-plus",
            "sh",
            "-eu",
            "-c",
            "test ! -e /data/runtime/supervisor/active/cpa/selection.json && "
            'test -z "$(ls -A /data/runtime/supervisor/artifacts/cpa)"',
        )
        evidence["stepB_tamperedUpload"] = {
            "uploadStatus": tampered_code,
            "uploadResponse": tampered_resp,
            "statusAfterTamperedUpload": after_tampered,
        }

        # Step (c): POST acquire {"source":"github"}, poll GET until succeeded -> active.origin=="acquired", LICENSE in stage dir.
        acq_code, acq_resp = public_http(
            slim_origin,
            bootstrap_api + "runtime/artifact/acquire",
            method="POST",
            body={"source": "github"},
            auth_scheme="Fresh",
            auth_token=session_token,
        )
        assert acq_code in (200, 202) and isinstance(acq_resp, dict), (acq_code, acq_resp)

        acquire_deadline = time.time() + 300
        settled_artifact = None
        while time.time() < acquire_deadline:
            code, cur = public_http(
                slim_origin,
                bootstrap_api + "runtime/artifact",
                auth_scheme="Fresh",
                auth_token=session_token,
            )
            assert code == 200 and isinstance(cur, dict), (code, cur)
            op = cur.get("operation") or {}
            if op.get("phase") in ("succeeded", "failed"):
                settled_artifact = cur
                break
            time.sleep(0.5)
        assert settled_artifact is not None, "timed out waiting for GitHub acquisition to settle"
        assert (settled_artifact.get("operation") or {}).get("phase") == "succeeded", settled_artifact
        assert (settled_artifact.get("operation") or {}).get("code") == "", settled_artifact
        assert settled_artifact.get("required") is False, settled_artifact
        assert settled_artifact.get("active") == {
            "artifactId": expected_artifact_id,
            "origin": "acquired",
        }, settled_artifact

        stage_dir = f"/data/runtime/supervisor/artifacts/cpa/{VERSION}"
        slim_dc(
            "exec",
            "-T",
            "cpa-manager-plus",
            "sh",
            "-eu",
            "-c",
            f"test -s {stage_dir}/LICENSE && test -x {stage_dir}/cli-proxy-api && "
            f"test -s {stage_dir}/artifact.json && test -s /data/runtime/supervisor/active/cpa/selection.json",
        )
        evidence["stepC_acquiredArtifact"] = settled_artifact

        # Step (d): Run Fresh New to healthy through public Ingress; verify CPA runs as uid 10001 from staged executable.
        code, gen_resp = public_http(
            slim_origin,
            bootstrap_api + "admin/generate",
            method="POST",
            body={},
            auth_scheme="Fresh",
            auth_token=session_token,
        )
        assert code == 200 and isinstance(gen_resp, dict) and gen_resp.get("key"), (code, gen_resp)
        admin_key = gen_resp["key"]
        secret_values.append(admin_key)

        for resource, payload in (
            ("admin/commit", {"identity": handoff["identity"], "commitId": secrets.token_hex(16), "key": admin_key}),
            ("routing/prepare", {"identity": handoff["identity"], "target": "/console"}),
            ("routing/activate", {"identity": handoff["identity"]}),
        ):
            code, op_state = public_http(
                slim_origin,
                bootstrap_api + resource,
                method="POST",
                body=payload,
                auth_scheme="Fresh",
                auth_token=session_token,
            )
            assert code == 200 and isinstance(op_state, dict), (resource, code, op_state)

        console_api = "/console/api/v2/"
        code, rt_info = public_http(
            slim_origin,
            console_api + "installation/runtime",
            auth_scheme="Fresh",
            auth_token=session_token,
        )
        assert code == 200 and isinstance(rt_info, dict), (code, rt_info)
        assert (rt_info.get("activeGatewayArtifact") or {}).get("artifactId") == expected_artifact_id, rt_info

        for _ in range(4):
            code, op_state = public_http(
                slim_origin,
                console_api + "strategy",
                method="POST",
                body={
                    "identity": op_state["identity"],
                    "expectedRevision": op_state["revision"],
                    "strategy": "new_managed",
                    "expectedRuntimeIdentity": rt_info["runtimeIdentity"],
                    "expectedRuntimeGeneration": rt_info["runtimeGeneration"],
                    "expectedArtifactId": expected_artifact_id,
                },
                auth_scheme="Fresh",
                auth_token=session_token,
            )
            assert code == 200 and isinstance(op_state, dict), ("strategy", code, op_state)
            if (op_state.get("strategy") or {}).get("stage") == "committed":
                break
        assert (op_state.get("strategy") or {}).get("stage") == "committed", op_state

        code, completed = public_http(
            slim_origin,
            console_api + "complete",
            method="POST",
            body={"identity": op_state["identity"], "expectedRevision": op_state["revision"]},
            auth_scheme="Fresh",
            auth_token=session_token,
        )
        assert code == 200 and isinstance(completed, dict) and completed.get("productComplete") is True, (code, completed)

        wait_until(
            lambda: inspect_state(slim_cid) == ("running", "healthy"),
            "slim container healthy after Fresh New completion",
            timeout=60,
        )
        health_code, health_body = public_http(slim_origin, "/health")
        assert health_code == 200, (health_code, health_body)
        models_code, _ = public_http(slim_origin, "/v1/models", auth_scheme="Bearer", auth_token="invalid-client-key")
        assert models_code == 401, models_code

        code, rt_status = public_http(
            slim_origin,
            console_api + "runtime/status",
            auth_scheme="Bearer",
            auth_token=admin_key,
        )
        assert code == 200 and isinstance(rt_status, dict) and rt_status.get("status") == "ready", (code, rt_status)

        proc_info = inspect_cpa_process(slim_dc)
        expected_staged_exe = f"{stage_dir}/cli-proxy-api"
        assert proc_info.get("EXE") == expected_staged_exe, proc_info
        assert proc_info.get("SHA") == pin, proc_info
        assert proc_info.get("Uid") == "Uid:\t10001\t10001\t10001\t10001", proc_info
        assert proc_info.get("Gid") == "Gid:\t10001\t10001\t10001\t10001", proc_info
        evidence["stepD_freshNewHealthy"] = {
            "runtimeStatus": rt_status,
            "cpaProcess": proc_info,
            "gatewayModelsInvalidKeyStatus": models_code,
        }

        # Step (e): docker restart -> GET immediately shows active acquired with operation=null, and gateway is healthy again.
        run(["docker", "restart", slim_cid])
        slim_origin = "http://" + slim_dc("port", "cpa-manager-plus", "18317").strip().splitlines()[-1]

        def fetch_restarted_artifact():
            c, body = public_http(
                slim_origin,
                console_api + "runtime/artifact",
                auth_scheme="Bearer",
                auth_token=admin_key,
            )
            return body if c == 200 and isinstance(body, dict) else None

        restarted_artifact = wait_until(
            fetch_restarted_artifact,
            "GET runtime/artifact after docker restart",
            timeout=60,
        )
        assert restarted_artifact.get("required") is False, restarted_artifact
        assert restarted_artifact.get("active") == {
            "artifactId": expected_artifact_id,
            "origin": "acquired",
        }, restarted_artifact
        assert restarted_artifact.get("operation") is None, restarted_artifact

        # Fresh complete (apps/manager-server/cmd/cpa-manager-plus/fresh.go:552-571) does not write runtime_desired_v1 and Reconciler.ReconcileOnce (internal/service/runtime/reconciler.go:92-122) omits expectedActiveArtifactId, so both full and slim require an explicit start via submitRuntimeOperation (fresh_runtime.go:715-727,854-861) after container restart.
        start_code, start_op = public_http(
            slim_origin,
            console_api + "runtime/operations",
            method="POST",
            body={"action": "start", "requestId": "s2a-restart-start"},
            auth_scheme="Bearer",
            auth_token=admin_key,
        )
        assert start_code in (200, 202) and isinstance(start_op, dict), (start_code, start_op)
        wait_until(
            lambda: inspect_state(slim_cid) == ("running", "healthy"),
            "slim container healthy after docker restart",
            timeout=90,
        )
        wait_until(
            lambda: (
                public_http(slim_origin, console_api + "runtime/status", auth_scheme="Bearer", auth_token=admin_key)[1]
                or {}
            ).get("status")
            == "ready",
            "Gateway ready after docker restart",
            timeout=60,
        )
        restarted_proc = inspect_cpa_process(slim_dc)
        assert restarted_proc.get("EXE") == expected_staged_exe and restarted_proc.get("SHA") == pin, restarted_proc
        evidence["stepE_afterRestart"] = {
            "artifactStatus": restarted_artifact,
            "cpaProcess": restarted_proc,
        }

        slim_dc("down", "-v", "--remove-orphans", check=False)

        # Step (f): full image, fresh volume -> GET returns required=false and active.origin=="bundled".
        full_port = pick_smoke_port()
        full_dc, full_override = make_dc(full_project, full_meta, full_port, "full")
        full_dc("up", "-d", "--no-build", "--pull", "never")
        full_origin = "http://" + full_dc("port", "cpa-manager-plus", "18317").strip().splitlines()[-1]
        _, full_session_token, _ = claim_fresh_session(full_dc, full_origin, secret_values)

        code, full_artifact = public_http(
            full_origin,
            bootstrap_api + "runtime/artifact",
            auth_scheme="Fresh",
            auth_token=full_session_token,
        )
        assert code == 200 and isinstance(full_artifact, dict), (code, full_artifact)
        assert full_artifact.get("required") is False, full_artifact
        assert full_artifact.get("active") == {
            "artifactId": expected_artifact_id,
            "origin": "bundled",
        }, full_artifact
        assert full_artifact.get("operation") is None, full_artifact
        evidence["stepF_fullImageArtifact"] = full_artifact

        evidence["result"] = "PASS"
    finally:
        for name, dc_fn in (("slim", slim_dc), ("full", full_dc)):
            if dc_fn is None:
                continue
            try:
                (out / f"compose-{name}.log").write_text(
                    redact(dc_fn("logs", "--no-color", check=False), secret_values),
                    encoding="utf-8",
                )
            finally:
                dc_fn("down", "-v", "--remove-orphans", check=False)
        for ov in (slim_override, full_override):
            if ov is not None:
                ov.unlink(missing_ok=True)
        (out / "evidence.json").write_text(
            json.dumps(redact(evidence, secret_values), indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )

    print(json.dumps(redact(evidence, secret_values), indent=2, sort_keys=True))


if __name__ == "__main__":
    main(sys.argv[1:])
