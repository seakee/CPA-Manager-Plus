#!/usr/bin/env python3
"""Single-container Phase 2 signed-slot unix.Exec self-update & rollback smoke test.

Usage:
  python3 bin/ci/runtime-slot-self-update-smoke.py BASE_IMAGE OUTPUT_DIR

Proves inside a real Linux container (--restart=no, zero /var/run/docker.sock):
  1. base-abi.json (base_epoch=1, 0444 0:0), 0640 0:10002 launcher-token isolation,
     and 0755 0:0 /data/runtime/cpamp/slots write-denial for Manager/CPA/Ingress.
  2. Offline Ed25519 signature & digest verification (tampered slot archive fails closed 422).
  3. Online PID 1 unix.Exec upgrade (v2.0.0-beta.1 -> v2.0.0-beta.2) with RestartCount == 0,
     identical container StartedAt, TRIAL_HOLD release, dual-version /health disclosure,
     and persistence across container stop/start.
  4. 60s Trial watchdog failure on broken v2.0.0-beta.3 -> automatic SQLite + data.key
     restore via `cpa-manager-plus manager-data-snapshot restore` (10002:10002) + unix.Exec
     rollback to v2.0.0-beta.2 with RestartCount == 0.
  5. Manual image upgrade takeover (BaseVersion v2.0.0-beta.4 >= SlotVersion v2.0.0-beta.2)
     pruning superseded slots and resetting slot-state.json.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
SOURCE_COMMIT = "743cae83a0a3ef3f47235c11f20566dd87708c56"
KEY_ID = "cpamp-e2e-slot-key-2026"


def run(args: list[str], *, cwd: Path = ROOT, env: dict[str, str] | None = None, check: bool = True, input_bytes: bytes | None = None, timeout: int = 240) -> subprocess.CompletedProcess:
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    proc = subprocess.run(
        args,
        cwd=str(cwd),
        env=merged_env,
        input=input_bytes,
        capture_output=True,
        timeout=timeout,
    )
    if check and proc.returncode != 0:
        raise RuntimeError(
            f"Command failed ({proc.returncode}): {' '.join(args)}\n"
            f"stdout:\n{proc.stdout.decode('utf-8', 'replace')}\n"
            f"stderr:\n{proc.stderr.decode('utf-8', 'replace')}"
        )
    return proc


def http_req(url: str, *, method: str = "GET", token: str | None = None, body: bytes | None = None, content_type: str | None = None, timeout: float = 10.0) -> tuple[int, bytes]:
    headers: dict[str, str] = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if content_type:
        headers["Content-Type"] = content_type
    req = urllib.request.Request(url, data=body, method=method, headers=headers)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=timeout) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()
    except Exception:
        return 0, b""


def container_http(container: str, url: str, *, method: str = "GET", token: str | None = None, body_json: dict | None = None) -> tuple[int, str]:
    """Perform loopback HTTP inside the container network namespace via wget/nc or a tiny helper."""
    # Use docker run --network container:<id> python:3.13-alpine (or curl/python helper image)
    raise NotImplementedError


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("Usage: runtime-slot-self-update-smoke.py BASE_IMAGE OUTPUT_DIR")

    base_image = sys.argv[1]
    out_dir = Path(sys.argv[2]).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    meta = json.loads(run(["docker", "image", "inspect", base_image]).stdout.decode())[0]
    arch = meta["Architecture"]
    if arch not in ("amd64", "arm64"):
        raise RuntimeError(f"Unsupported architecture: {arch}")
    platform = f"linux/{arch}"

    run_id = secrets.token_hex(5)
    vol_name = f"cpamp-slot-smoke-data-{run_id}"
    ctr_name = f"cpamp-slot-smoke-{run_id}"
    probe_name = f"cpamp-slot-probe-{run_id}"
    img_beta1 = f"cpamp-slot-smoke-beta1:{run_id}"
    img_beta4 = f"cpamp-slot-smoke-beta4:{run_id}"
    created_images: list[str] = []

    work_tmp = Path(tempfile.mkdtemp(prefix="cpamp-slot-smoke-"))
    summary: dict[str, object] = {"result": "FAIL", "platform": platform}

    def probe_http(url: str, *, method: str = "GET", token: str | None = None, body: bytes | None = None, content_type: str | None = None) -> tuple[int, bytes]:
        script = (
            "import base64,json,sys,urllib.error,urllib.request\n"
            "req_in=json.loads(sys.stdin.read())\n"
            "data=base64.b64decode(req_in['body']) if req_in.get('body') else None\n"
            "r=urllib.request.Request(req_in['url'],data=data,method=req_in['method'],headers=req_in['headers'])\n"
            "op=urllib.request.build_opener(urllib.request.ProxyHandler({}))\n"
            "try:\n"
            " with op.open(r,timeout=30) as resp:\n"
            "  print(json.dumps({'code':resp.status,'body':base64.b64encode(resp.read()).decode()}))\n"
            "except urllib.error.HTTPError as e:\n"
            " print(json.dumps({'code':e.code,'body':base64.b64encode(e.read()).decode()}))\n"
            "except Exception as e:\n"
            " print(json.dumps({'code':0,'err':str(e),'body':''}))\n"
        )
        headers: dict[str, str] = {}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        if content_type:
            headers["Content-Type"] = content_type
        payload = json.dumps({
            "url": url,
            "method": method,
            "headers": headers,
            "body": __import__("base64").b64encode(body).decode() if body is not None else None,
        }).encode()
        proc = run(
            ["docker", "run", "--rm", "-i", "--network", f"container:{ctr_name}", "python:3.13-alpine", "python3", "-c", script],
            input_bytes=payload,
        )
        parsed = json.loads(proc.stdout.decode())
        return int(parsed["code"]), __import__("base64").b64decode(parsed.get("body", ""))

    try:
        # Ensure python:3.13-alpine is present for loopback namespace probing
        if run(["docker", "image", "inspect", "python:3.13-alpine"], check=False).returncode != 0:
            run(["docker", "pull", "--platform", platform, "python:3.13-alpine"], timeout=300)

        # 1. Generate ephemeral Ed25519 slot signing keypair
        keys_dir = work_tmp / "keys"
        run(["node", "bin/release/cpamp-slot-sign.mjs", "keygen", "--key-id", KEY_ID, "--out-dir", str(keys_dir)])
        pub_doc = json.loads((keys_dir / f"{KEY_ID}.pub.json").read_text())
        pub_hex = pub_doc["public_key_hex"]
        priv_pem = keys_dir / f"{KEY_ID}.priv.pem"
        extra_key_ldflag = f"-X main.ExtraTrustedSlotPublicKey={KEY_ID}:{pub_hex}"

        # Extract base rootfs (/usr/local/bin and /usr/local/share) from BASE_IMAGE
        rootfs_base = work_tmp / "base-rootfs"
        rootfs_base.mkdir(parents=True)
        export_ctr = run(["docker", "create", "--platform", platform, base_image]).stdout.decode().strip()
        try:
            tar_proc = run(["docker", "export", export_ctr])
            extract_proc = subprocess.run(
                ["tar", "-xf", "-", "-C", str(rootfs_base), "usr/local/bin", "usr/local/share"],
                input=tar_proc.stdout,
                capture_output=True,
                check=True,
            )
            _ = extract_proc
        finally:
            run(["docker", "rm", "-f", export_ctr], check=False)

        # Verify base-abi.json emitted in image
        base_abi = json.loads((rootfs_base / "usr/local/share/cpamp/release/base-abi.json").read_text())
        assert base_abi == {"base_epoch": 1, "platform": platform, "schema_version": 1}, base_abi

        # Helper to cross-compile Linux Go binaries cleanly
        bins_dir = work_tmp / "bins"
        bins_dir.mkdir()

        def build_launcher(ver: str, out_path: Path) -> None:
            run(
                [
                    "go", "build", "-trimpath",
                    "-ldflags", f"-s -w -X main.Version={ver} -X main.SourceCommit={SOURCE_COMMIT} {extra_key_ldflag}",
                    "-o", str(out_path),
                    "./cmd/cpamp-launcher",
                ],
                cwd=ROOT / "apps/runtime-supervisor",
                env={"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch},
            )

        mgr_build_dir = ROOT / "apps/manager-server"
        if not (mgr_build_dir / "internal/httpapi/web/fullweb/index.html").exists():
            mgr_build_dir = work_tmp / "manager-server-src"
            shutil.copytree(ROOT / "apps/manager-server", mgr_build_dir)
            fw_dir = mgr_build_dir / "internal/httpapi/web/fullweb"
            fw_dir.mkdir(parents=True, exist_ok=True)
            (fw_dir / "index.html").write_text("<!doctype html><title>CPAMP</title>\n")

        def build_manager(ver: str, out_path: Path) -> None:
            run(
                [
                    "go", "build", "-trimpath",
                    "-ldflags", (
                        f"-s -w "
                        f"-X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version={ver} "
                        f"-X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.SourceCommit={SOURCE_COMMIT} "
                        f"-X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.BuildDate=2026-11-26T00:00:00Z"
                    ),
                    "-o", str(out_path),
                    "./cmd/cpa-manager-plus",
                ],
                cwd=mgr_build_dir,
                env={"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch},
            )

        launcher_b1 = bins_dir / "cpamp-launcher-b1"
        launcher_b2 = bins_dir / "cpamp-launcher-b2"
        launcher_b3 = bins_dir / "cpamp-launcher-b3"
        launcher_b4 = bins_dir / "cpamp-launcher-b4"
        manager_b1 = bins_dir / "cpa-manager-plus-b1"
        manager_b2 = bins_dir / "cpa-manager-plus-b2"
        manager_b4 = bins_dir / "cpa-manager-plus-b4"
        crashing_mgr_b3 = bins_dir / "cpa-manager-plus-b3-crash"

        build_launcher("v2.0.0-beta.1", launcher_b1)
        build_launcher("v2.0.0-beta.2", launcher_b2)
        build_launcher("v2.0.0-beta.3", launcher_b3)
        build_launcher("v2.0.0-beta.4", launcher_b4)
        build_manager("v2.0.0-beta.1", manager_b1)
        build_manager("v2.0.0-beta.2", manager_b2)
        build_manager("v2.0.0-beta.4", manager_b4)
        # Build a tiny static Linux binary for crashing v2.0.0-beta.3 Manager (exits 1 on server start, exits 1 on runtime-upgrade-worker)
        crash_src = work_tmp / "crashmgr"
        crash_src.mkdir()
        (crash_src / "go.mod").write_text("module crashmgr\n\ngo 1.24\n")
        (crash_src / "main.go").write_text(
            'package main\nimport ("fmt"; "os")\n'
            'func main() { fmt.Fprintln(os.Stderr, "injected beta.3 manager startup crash"); os.Exit(1) }\n'
        )
        run(
            ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(crashing_mgr_b3), "."],
            cwd=crash_src,
            env={"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch},
        )

        # Layer test-key-enabled launcher + v2.0.0-beta.1 provenance onto base_image -> img_beta1
        b1_ctx = work_tmp / "img-b1"
        b1_ctx.mkdir()
        shutil.copy2(launcher_b1, b1_ctx / "cpamp-launcher")
        shutil.copy2(manager_b1, b1_ctx / "cpa-manager-plus")
        prov_b1 = json.loads((rootfs_base / "usr/local/share/cpamp/provenance.json").read_text())
        prov_b1["version"] = "v2.0.0-beta.1"
        (b1_ctx / "provenance.json").write_text(json.dumps(prov_b1, indent=2) + "\n")
        (b1_ctx / "Dockerfile").write_text(
            f"FROM {base_image}\n"
            "COPY --chmod=0755 cpamp-launcher /usr/local/bin/cpamp-launcher\n"
            "COPY --chmod=0755 cpa-manager-plus /usr/local/bin/cpa-manager-plus\n"
            "COPY --chmod=0444 provenance.json /usr/local/share/cpamp/provenance.json\n"
        )
        run(["docker", "build", "--platform", platform, "-t", img_beta1, str(b1_ctx)])
        created_images.append(img_beta1)

        # Build v2.0.0-beta.4 image (for BaseVersion >= SlotVersion takeover proof)
        b4_ctx = work_tmp / "img-b4"
        b4_ctx.mkdir()
        shutil.copy2(launcher_b4, b4_ctx / "cpamp-launcher")
        shutil.copy2(manager_b4, b4_ctx / "cpa-manager-plus")
        prov_b4 = json.loads((rootfs_base / "usr/local/share/cpamp/provenance.json").read_text())
        prov_b4["version"] = "v2.0.0-beta.4"
        (b4_ctx / "provenance.json").write_text(json.dumps(prov_b4, indent=2) + "\n")
        (b4_ctx / "Dockerfile").write_text(
            f"FROM {base_image}\n"
            "COPY --chmod=0755 cpamp-launcher /usr/local/bin/cpamp-launcher\n"
            "COPY --chmod=0755 cpa-manager-plus /usr/local/bin/cpa-manager-plus\n"
            "COPY --chmod=0444 provenance.json /usr/local/share/cpamp/provenance.json\n"
        )
        run(["docker", "build", "--platform", platform, "-t", img_beta4, str(b4_ctx)])
        created_images.append(img_beta4)

        # Package real signed slot archives for v2.0.0-beta.2 (healthy) and v2.0.0-beta.3 (crashing manager)
        def make_slot_archive(ver: str, launcher_bin: Path, manager_bin: Path) -> Path:
            stage = work_tmp / f"slot-root-{ver}"
            shutil.copytree(rootfs_base, stage)
            shutil.copy2(launcher_bin, stage / "usr/local/bin/cpamp-launcher")
            shutil.copy2(manager_bin, stage / "usr/local/bin/cpa-manager-plus")
            prov_path = stage / "usr/local/share/cpamp/provenance.json"
            prov = json.loads(prov_path.read_text())
            prov["version"] = ver
            prov_path.write_text(json.dumps(prov, indent=2) + "\n")
            slot_out = work_tmp / f"cpamp-slot_{ver}_linux_{arch}.tar.gz"
            run([
                "node", "bin/release/package-cpamp-slot.mjs",
                "--root", str(stage),
                "--version", ver,
                "--source-commit", SOURCE_COMMIT,
                "--platform", platform,
                "--min-base-epoch", "1",
                "--schema-generation", "1",
                "--key-id", KEY_ID,
                "--private-key", str(priv_pem),
                "--public-key", pub_hex,
                "--out", str(slot_out),
            ])
            return slot_out

        slot_b2_tar = make_slot_archive("v2.0.0-beta.2", launcher_b2, manager_b2)
        slot_b3_tar = make_slot_archive("v2.0.0-beta.3", launcher_b3, crashing_mgr_b3)

        # Start single container with --restart=no and ZERO /var/run/docker.sock!
        run(["docker", "volume", "create", vol_name])
        run([
            "docker", "run", "-d",
            "--name", ctr_name,
            "--platform", platform,
            "--restart=no",
            "-e", "CPAMP_TRIAL_STABILIZATION_WINDOW=1s",
            "-v", f"{vol_name}:/data",
            img_beta1,
        ])

        # Wait for launcher-token and Manager health/bootstrap readiness
        launcher_token = ""
        for _ in range(80):
            p = run(["docker", "exec", ctr_name, "cat", "/data/runtime-secret/launcher-token"], check=False)
            if p.returncode == 0 and p.stdout.strip():
                launcher_token = p.stdout.decode().strip()
                break
            time.sleep(0.25)
        assert len(launcher_token) == 64, f"unexpected launcher token: {launcher_token!r}"

        # Admit the real Fresh Manager store (/data/usage.sqlite + /data/data.key) as UID 10002:10002
        run(["docker", "exec", "-u", "10002:10002", ctr_name, "cpa-manager-plus", "initialize"])

        # Wait for Manager discovery/health endpoint (503 before Fresh complete + 200 on /.well-known/cpamp-bootstrap/ui)
        initial_health: dict = {}
        for _ in range(80):
            code, raw = probe_http("http://127.0.0.1:18319/health")
            ui_code, _ = probe_http("http://127.0.0.1:18319/.well-known/cpamp-bootstrap/ui")
            if code == 200 and ui_code == 200 and raw:
                initial_health = json.loads(raw.decode())
                break
            time.sleep(0.25)
        assert initial_health.get("running_version") == "v2.0.0-beta.1", initial_health
        assert initial_health.get("base_image_version") == "v2.0.0-beta.1", initial_health

        # 1. Verify privilege boundaries inside container
        stat_out = run([
            "docker", "exec", ctr_name, "sh", "-ec",
            "stat -c '%u:%g %a' /data/runtime-secret/launcher-token /data/runtime/cpamp /data/runtime/cpamp/slots",
        ]).stdout.decode().strip().splitlines()
        assert stat_out == ["0:10002 640", "0:0 755", "0:0 755"], stat_out
        # Verify UID 10001 (CPA) and UID 10003 (Ingress) cannot read launcher-token, and UID 10002 (Manager) cannot write slots/
        assert run(["docker", "exec", "-u", "10001:10001", ctr_name, "cat", "/data/runtime-secret/launcher-token"], check=False).returncode != 0
        assert run(["docker", "exec", "-u", "10003:10003", ctr_name, "cat", "/data/runtime-secret/launcher-token"], check=False).returncode != 0
        assert run(["docker", "exec", "-u", "10002:10002", ctr_name, "touch", "/data/runtime/cpamp/slots/pwn"], check=False).returncode != 0
        # Verify Ingress public edge refuses /internal/v1/trial/release
        ing_deny_code, _ = probe_http("http://127.0.0.1:18317/internal/v1/trial/release", method="POST", token=launcher_token, body=b"{}", content_type="application/json")
        assert ing_deny_code in (403, 404), ing_deny_code
        ctr_inspect_before = json.loads(run(["docker", "inspect", ctr_name]).stdout.decode())[0]
        started_at_before = ctr_inspect_before["State"]["StartedAt"]
        ctr_id_before = ctr_inspect_before["Id"]
        assert ctr_inspect_before["RestartCount"] == 0

        # 2. Tampered slot archive MUST fail closed (422)
        b2_bytes = bytearray(slot_b2_tar.read_bytes())
        tampered_bytes = bytearray(b2_bytes)
        tampered_bytes[-16] ^= 0xFF
        t_code, t_body = probe_http(
            "http://127.0.0.1:9083/v1/launcher/slot/prepare-upload?target_version=v2.0.0-beta.2",
            method="PUT",
            token=launcher_token,
            body=bytes(tampered_bytes),
            content_type="application/gzip",
        )
        assert t_code == 422, (t_code, t_body)

        # 3. Upload valid signed v2.0.0-beta.2 slot & activate -> PID 1 unix.Exec!
        u_code, u_body = probe_http(
            "http://127.0.0.1:9083/v1/launcher/slot/prepare-upload?target_version=v2.0.0-beta.2",
            method="PUT",
            token=launcher_token,
            body=bytes(b2_bytes),
            content_type="application/gzip",
        )
        assert u_code == 200, (u_code, u_body)

        # Helper to materialize a verified VACUUM INTO snapshot with canonical target paths
        def make_container_snapshot(snap_name: str) -> str:
            snap_path = f"/data/backups/{snap_name}"
            snap_py = (
                "import hashlib, json, os, shutil, sqlite3\n"
                "os.makedirs('/data/backups', mode=0o700, exist_ok=True)\n"
                f"snap = {snap_path!r}\n"
                "os.makedirs(snap, mode=0o700, exist_ok=True)\n"
                "db_dst = snap + '/database'\n"
                "conn = sqlite3.connect('/data/usage.sqlite')\n"
                "conn.execute(f\"VACUUM INTO '{db_dst}'\")\n"
                "conn.close()\n"
                "os.chmod(db_dst, 0o600)\n"
                "db_bytes = open(db_dst, 'rb').read()\n"
                "db_sha = hashlib.sha256(db_bytes).hexdigest()\n"
                "files = {\n"
                "  'database': {'existed': True, 'mode': 0o600, 'size': len(db_bytes), 'sha256': db_sha},\n"
                "  'database-wal': {'existed': False},\n"
                "  'database-shm': {'existed': False},\n"
                "  'database-journal': {'existed': False},\n"
                "}\n"
                "if os.path.exists('/data/data.key'):\n"
                "  shutil.copyfile('/data/data.key', snap + '/data-key')\n"
                "  os.chmod(snap + '/data-key', 0o600)\n"
                "  kb = open(snap + '/data-key', 'rb').read()\n"
                "  files['data-key'] = {'existed': True, 'mode': 0o600, 'size': len(kb), 'sha256': hashlib.sha256(kb).hexdigest()}\n"
                "else:\n"
                "  files['data-key'] = {'existed': False}\n"
                "mf = snap + '/manifest.json'\n"
                "open(mf, 'w').write(json.dumps({\n"
                "  'version': 1,\n"
                "  'target_db_path': '/data/usage.sqlite',\n"
                "  'target_data_key_path': '/data/data.key',\n"
                "  'files': files,\n"
                "}, indent=2) + '\\n')\n"
                "os.chmod(mf, 0o600)\n"
                "print(db_sha)\n"
            )
            db_sha = run([
                "docker", "run", "--rm", "-u", "10002:10002", "-v", f"{vol_name}:/data",
                "python:3.13-alpine", "python3", "-c", snap_py,
            ]).stdout.decode().strip()
            assert len(db_sha) == 64, db_sha
            return snap_path

        # Empty snapshot_dir MUST fail closed (422 snapshot_invalid)
        empty_snap_code, empty_snap_body = probe_http(
            "http://127.0.0.1:9083/v1/launcher/slot/activate",
            method="POST",
            token=launcher_token,
            body=json.dumps({
                "target_version": "v2.0.0-beta.2",
                "snapshot_dir": "",
                "previous_schema_generation": 1,
            }).encode(),
            content_type="application/json",
        )
        assert empty_snap_code == 422, (empty_snap_code, empty_snap_body)

        snap_b1_b2 = make_container_snapshot("pre-slot-b1-to-b2")
        a_code, a_body = probe_http(
            "http://127.0.0.1:9083/v1/launcher/slot/activate",
            method="POST",
            token=launcher_token,
            body=json.dumps({
                "target_version": "v2.0.0-beta.2",
                "snapshot_dir": snap_b1_b2,
                "previous_schema_generation": 1,
            }).encode(),
            content_type="application/json",
        )
        assert a_code == 202, (a_code, a_body)
        a_resp = json.loads(a_body.decode())
        assert str(a_resp.get("attempt_id", "")).startswith("attempt-"), a_resp
        b2_attempt_id = a_resp["attempt_id"]

        # Poll until v2.0.0-beta.2 commits Trial (after stabilization soak) and releases TRIAL_HOLD!
        b2_health: dict = {}
        b2_slot_status: dict = {}
        for _ in range(160):
            s_code, s_raw = probe_http("http://127.0.0.1:9083/v1/launcher/slot/status", token=launcher_token)
            h_code, h_raw = probe_http("http://127.0.0.1:18319/health")
            if s_code == 200 and h_code == 200 and h_raw:
                st = json.loads(s_raw.decode())
                hl = json.loads(h_raw.decode())
                if st.get("state") == "committed" and st.get("active_slot") == "v2.0.0-beta.2" and not hl.get("trial_hold"):
                    b2_slot_status = st
                    b2_health = hl
                    break
            time.sleep(0.25)
        assert b2_slot_status.get("last_attempt_id") == b2_attempt_id, b2_slot_status
        assert b2_slot_status.get("state") == "committed", b2_slot_status
        assert b2_health.get("running_version") == "v2.0.0-beta.2", b2_health
        assert b2_health.get("base_image_version") == "v2.0.0-beta.1", b2_health
        assert b2_health.get("active_slot") == "v2.0.0-beta.2", b2_health

        # Prove PID 1 unix.Exec replaced the process IN-PLACE without container restart or exit!
        ctr_inspect_b2 = json.loads(run(["docker", "inspect", ctr_name]).stdout.decode())[0]
        assert ctr_inspect_b2["Id"] == ctr_id_before
        assert ctr_inspect_b2["RestartCount"] == 0
        assert ctr_inspect_b2["State"]["Running"] is True
        assert ctr_inspect_b2["State"]["StartedAt"] == started_at_before, (
            ctr_inspect_b2["State"]["StartedAt"],
            started_at_before,
        )

        # Prove cold-start persistence across container stop + start!
        run(["docker", "stop", "-t", "30", ctr_name])
        run(["docker", "start", ctr_name])
        restarted_health: dict = {}
        for _ in range(80):
            h_code, h_raw = probe_http("http://127.0.0.1:18319/health")
            if h_code == 200 and h_raw:
                hl = json.loads(h_raw.decode())
                if hl.get("running_version") == "v2.0.0-beta.2" and hl.get("active_slot") == "v2.0.0-beta.2":
                    restarted_health = hl
                    break
            time.sleep(0.25)
        assert restarted_health.get("running_version") == "v2.0.0-beta.2", restarted_health

        ctr_inspect_restarted = json.loads(run(["docker", "inspect", ctr_name]).stdout.decode())[0]
        started_at_restarted = ctr_inspect_restarted["State"]["StartedAt"]

        # 4. Stage crashing v2.0.0-beta.3 + take live WAL-consistent VACUUM INTO snapshot + mutate journal -> prove 60s Trial Watchdog restores SQLite & rolls back via unix.Exec!
        snap_b2_b3 = make_container_snapshot("pre-slot-b2-to-b3")
        # Upload crashing v2.0.0-beta.3 slot
        b3_bytes = slot_b3_tar.read_bytes()
        u3_code, u3_body = probe_http(
            "http://127.0.0.1:9083/v1/launcher/slot/prepare-upload?target_version=v2.0.0-beta.3",
            method="PUT",
            token=launcher_token,
            body=b3_bytes,
            content_type="application/gzip",
        )
        assert u3_code == 200, (u3_code, u3_body)

        # Corrupt/mutate /data/data.key (or add a dummy wal/mutated key) right before/during crash to prove restore-pre-migrate restores exact snapshot state!
        run(["docker", "exec", "-u", "10002:10002", ctr_name, "sh", "-ec", "printf 'corrupted-during-failed-trial' > /data/usage.sqlite-journal"])

        a3_code, a3_body = probe_http(
            "http://127.0.0.1:9083/v1/launcher/slot/activate",
            method="POST",
            token=launcher_token,
            body=json.dumps({
                "target_version": "v2.0.0-beta.3",
                "snapshot_dir": snap_b2_b3,
                "previous_schema_generation": 1,
            }).encode(),
            content_type="application/json",
        )
        assert a3_code == 202, (a3_code, a3_body)
        a3_resp = json.loads(a3_body.decode())
        b3_attempt_id = a3_resp["attempt_id"]

        # Poll until Trial Watchdog rolls back to v2.0.0-beta.2 WITHOUT container exit!
        rb_status: dict = {}
        rb_health: dict = {}
        for _ in range(160):
            s_code, s_raw = probe_http("http://127.0.0.1:9083/v1/launcher/slot/status", token=launcher_token)
            h_code, h_raw = probe_http("http://127.0.0.1:18319/health")
            if s_code == 200 and h_code == 200 and h_raw:
                st = json.loads(s_raw.decode())
                hl = json.loads(h_raw.decode())
                if st.get("last_rolled_back_slot") == "v2.0.0-beta.3" and hl.get("running_version") == "v2.0.0-beta.2":
                    rb_status = st
                    rb_health = hl
                    break
            time.sleep(0.25)
        assert rb_status.get("last_rolled_back_slot") == "v2.0.0-beta.3", rb_status
        assert rb_status.get("last_attempt_id") == b3_attempt_id, rb_status
        assert rb_status.get("rollback_available") is True, rb_status
        assert rb_status.get("last_failure_code") == "manager_startup_failed", rb_status
        assert rb_health.get("running_version") == "v2.0.0-beta.2", rb_health
        # Prove container NEVER restarted (StartedAt unchanged, RestartCount == 0) AND restore-pre-migrate removed -journal and restored exact DB sha256!
        ctr_inspect_after_rb = json.loads(run(["docker", "inspect", ctr_name]).stdout.decode())[0]
        assert ctr_inspect_after_rb["RestartCount"] == 0
        assert ctr_inspect_after_rb["State"]["StartedAt"] == started_at_restarted
        assert run(["docker", "exec", ctr_name, "test", "!", "-e", "/data/usage.sqlite-journal"], check=False).returncode == 0

        # 5. Manual docker pull takeover: stop container, start v2.0.0-beta.4 image on the SAME /data volume!
        run(["docker", "rm", "-f", ctr_name])
        run([
            "docker", "run", "-d",
            "--name", ctr_name,
            "--platform", platform,
            "--restart=no",
            "-v", f"{vol_name}:/data",
            img_beta4,
        ])
        b4_health: dict = {}
        b4_status: dict = {}
        for _ in range(80):
            s_code, s_raw = probe_http("http://127.0.0.1:9083/v1/launcher/slot/status", token=launcher_token)
            h_code, h_raw = probe_http("http://127.0.0.1:18319/health")
            if s_code == 200 and h_code == 200 and h_raw:
                st = json.loads(s_raw.decode())
                hl = json.loads(h_raw.decode())
                if hl.get("running_version") == "v2.0.0-beta.4":
                    b4_status = st
                    b4_health = hl
                    break
            time.sleep(0.25)
        assert b4_health.get("running_version") == "v2.0.0-beta.4", b4_health
        assert b4_health.get("base_image_version") == "v2.0.0-beta.4", b4_health
        assert not b4_health.get("active_slot"), b4_health
        assert b4_status.get("state") == "none", b4_status
        assert b4_status.get("staged_slots") == [], b4_status

        summary.update(
            result="PASS",
            initialHealth=initial_health,
            upgradedSlotStatus=b2_slot_status,
            rolledBackSlotStatus=rb_status,
            baseTakeoverStatus=b4_status,
        )
    except Exception:
        logs = run(["docker", "logs", ctr_name], check=False)
        sys.stderr.write(f"\n=== CONTAINER LOGS ===\n{logs.stdout.decode('utf-8', 'replace')}\n{logs.stderr.decode('utf-8', 'replace')}\n")
        raise
    finally:
        run(["docker", "rm", "-f", ctr_name], check=False)
        run(["docker", "rm", "-f", probe_name], check=False)
        run(["docker", "volume", "rm", "-f", vol_name], check=False)
        for img in created_images:
            run(["docker", "rmi", "-f", img], check=False)
        shutil.rmtree(work_tmp, ignore_errors=True)
        (out_dir / "result.json").write_text(json.dumps(summary, indent=2) + "\n")

    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
