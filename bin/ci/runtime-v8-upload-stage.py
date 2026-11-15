#!/usr/bin/env python3
"""Real Supervisor HTTP helper for CPA v8.0.55 Runtime lifecycle E2E scripts.

Provides loopback HTTP subcommands inside a running container without installing
extra tools in the production Alpine image, forging stage files, or exposing
Runtime/CPA credentials on host or container process command lines:

  upload-pinned <container_id> <bundled_hold_path> <runtime_root>
    Reads the Runtime transport token directly inside <container_id>, packages
    the container's own unmodified pinned CPA v8.0.55 executable and LICENSE
    into an in-memory .tar.gz, verifies GET /v1/runtime/artifact reports
    acquisition required, streams PUT /v1/runtime/artifact/upload, and verifies
    both active selection and finalized stage metadata (including exact
    sourceArchiveDigest). Prints the verified canonical artifactId.

  post-json <container_id> <path> --body-file <0600_json_file> [--out-file <path>]
    Reads the Runtime transport token inside <container_id> and the JSON request
    body from a 0600 regular file, sends an authenticated POST request to
    127.0.0.1:9081, and emits {"status": <http_code>, "body": <decoded_body>}.

  cpa-get <container_id> <path> [--bearer-file <0600_token_file>] [--out-file <path>]
    Sends an HTTP/1.1 GET request to 127.0.0.1:8317 inside <container_id> with
    an optional Bearer credential read from a 0600 regular file, and emits
    {"status": <http_code>, "body": <decoded_body>}.
"""

from __future__ import annotations

import gzip
import hashlib
import http.client
import io
import json
import os
import pathlib
import re
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading

EXPECTED_CPA_VERSION = "8.0.55"
ARTIFACT_ID_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
MANIFEST_PATH = "/usr/local/share/cpamp/runtime/cpa-artifact.json"
LICENSE_PATH = "/usr/local/share/licenses/cli-proxy-api/LICENSE"
SECRET_JSON_KEYS = frozenset(
    {
        "apikey",
        "api-key",
        "apikeys",
        "api-keys",
        "managementkey",
        "management-key",
        "secret-key",
        "secret",
        "token",
    }
)
REDACT_PATTERNS = (
    re.compile(r"(Authorization:\s*(?:Bearer|Fresh)\s+)[^\s\"']+", re.IGNORECASE),
    re.compile(r"(X-Management-Key:\s*)[^\s\"']+", re.IGNORECASE),
    re.compile(
        r"([\"']?(?:secret-key|CPAMP_RUNTIME_TOKEN|managementKey|token|apiKey|api-key)[\"']?\s*[:=]\s*[\"']?)[^\s\"',}\]]+",
        re.IGNORECASE,
    ),
)
CLIENT_KEY_RE = re.compile(r"\bsk-[A-Za-z0-9_-]{8,}\b")

class _PipeSocket:
    def __init__(self, stream: io.BufferedReader) -> None:
        self._stream = stream

    def makefile(self, mode: str = "rb", buffering: int = -1) -> io.BufferedReader:
        return self._stream


def redact_text(text: str, secrets: tuple[str, ...] = ()) -> str:
    out = text
    for secret in secrets:
        if secret:
            out = out.replace(secret, "[REDACTED]")
    for pattern in REDACT_PATTERNS:
        out = pattern.sub(r"\1[REDACTED]", out)
    return CLIENT_KEY_RE.sub("[REDACTED]", out)


def collect_json_secrets(value: object) -> list[str]:
    found: list[str] = []
    if isinstance(value, dict):
        for key, child in value.items():
            if isinstance(key, str) and key.lower() in SECRET_JSON_KEYS:
                if isinstance(child, str) and child:
                    found.append(child)
                elif isinstance(child, list):
                    found.extend(item for item in child if isinstance(item, str) and item)
            found.extend(collect_json_secrets(child))
    elif isinstance(value, list):
        for item in value:
            found.extend(collect_json_secrets(item))
    return found


def read_private_file(file_path: str, label: str) -> bytes:
    try:
        st = os.lstat(file_path)
    except OSError as err:
        raise SystemExit(f"cannot stat {label} {file_path!r}: {err}") from None
    if not stat.S_ISREG(st.st_mode):
        raise SystemExit(f"{label} must be a regular file (not a symlink): {file_path!r}")
    mode = stat.S_IMODE(st.st_mode)
    if mode != 0o600:
        raise SystemExit(f"{label} must have mode 0600, got {oct(mode)}: {file_path!r}")
    try:
        return pathlib.Path(file_path).read_bytes()
    except OSError as err:
        raise SystemExit(f"cannot read {label} {file_path!r}: {err}") from None


def write_output_json(out_file: str | None, payload: dict[str, object]) -> None:
    encoded = json.dumps(payload, separators=(",", ":"))
    if out_file is None:
        sys.stdout.write(encoded)
        return
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(out_file, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        data = encoded.encode("utf-8")
        view = memoryview(data)
        offset = 0
        while offset < len(view):
            offset += os.write(fd, view[offset:])
    finally:
        os.close(fd)


def run_checked(
    args: list[str],
    *,
    timeout: int = 60,
    secrets_to_redact: tuple[str, ...] = (),
) -> subprocess.CompletedProcess[bytes]:
    completed = subprocess.run(args, capture_output=True, timeout=timeout)
    if completed.returncode != 0:
        stderr = redact_text(
            completed.stderr.decode("utf-8", errors="replace").strip(),
            secrets_to_redact,
        )
        safe_args = [redact_text(arg, secrets_to_redact) for arg in args]
        raise SystemExit(f"command failed ({completed.returncode}): {safe_args!r}: {stderr}")
    return completed


def read_container_runtime_token(container_id: str) -> str:
    script = (
        'token_file="${CPAMP_RUNTIME_TOKEN_FILE:-}"; '
        'if [ -z "${token_file}" ] || [ ! -e "${token_file}" ]; then '
        "for c in /data/runtime-secret/token /run/cpamp/runtime-secret/token "
        "/data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do "
        'if [ -e "$c" ]; then token_file="$c"; break; fi; '
        "done; fi; "
        'test -n "${token_file}" && test -r "${token_file}" && cat "${token_file}"'
    )
    raw = run_checked(["docker", "exec", container_id, "sh", "-ec", script]).stdout
    token = raw.decode("utf-8", errors="replace").strip()
    if not token or any(ch.isspace() for ch in token):
        raise SystemExit("invalid or empty Runtime transport token inside container")
    return token


def supervisor_http(
    container_id: str,
    token: str | None,
    method: str,
    path: str,
    body: bytes | None = None,
    content_type: str | None = None,
    timeout: int = 180,
    *,
    port: int = 9081,
    include_runtime_features: bool = True,
    secrets_to_redact: tuple[str, ...] = (),
) -> tuple[int, object]:
    if timeout <= 0:
        raise SystemExit(f"invalid timeout {timeout}: must be > 0")
    if not path.startswith("/") or "\r" in path or "\n" in path:
        raise SystemExit(f"invalid HTTP request path: {path!r}")
    if "\r" in method or "\n" in method:
        raise SystemExit(f"invalid HTTP method: {method!r}")
    if token is not None and ("\r" in token or "\n" in token):
        raise SystemExit("invalid bearer token framing")
    if content_type is not None and ("\r" in content_type or "\n" in content_type):
        raise SystemExit(f"invalid Content-Type header: {content_type!r}")

    redacted_secrets = tuple(s for s in ((token or ""), *secrets_to_redact) if s)
    proc = subprocess.Popen(
        ["docker", "exec", "-i", container_id, "nc", "127.0.0.1", str(port)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None

    timed_out = threading.Event()

    def _kill_on_timeout() -> None:
        timed_out.set()
        try:
            proc.kill()
        except OSError:
            pass

    timer = threading.Timer(timeout, _kill_on_timeout)
    timer.daemon = True
    timer.start()

    http_err: Exception | None = None
    raw_body = b""
    status = 0
    stderr_text = ""
    returncode: int | None = None

    try:
        headers = [
            f"{method} {path} HTTP/1.1",
            f"Host: 127.0.0.1:{port}",
        ]
        if token is not None:
            headers.append(f"Authorization: Bearer {token}")
        if include_runtime_features:
            headers.append("X-CPAMP-Runtime-Features: active-gateway-artifact-v1")
        headers.append("Connection: close")
        if content_type is not None:
            headers.append(f"Content-Type: {content_type}")
        if body is not None:
            headers.append(f"Content-Length: {len(body)}")
        raw_head = ("\r\n".join(headers) + "\r\n\r\n").encode("ascii")

        try:
            proc.stdin.write(raw_head)
            if body:
                view = memoryview(body)
                offset = 0
                chunk_size = 65536
                while offset < len(view):
                    if timed_out.is_set():
                        break
                    proc.stdin.write(view[offset : offset + chunk_size])
                    offset += chunk_size
            if not timed_out.is_set():
                proc.stdin.flush()
        except (BrokenPipeError, OSError) as write_err:
            if timed_out.is_set():
                http_err = write_err

        if not timed_out.is_set() and http_err is None:
            response = http.client.HTTPResponse(_PipeSocket(proc.stdout), method=method)
            response.begin()
            raw_body = response.read()
            status = response.status
    except (http.client.HTTPException, OSError) as err:
        http_err = err
    finally:
        timer.cancel()
        try:
            proc.stdin.close()
        except OSError:
            pass
        try:
            returncode = proc.wait(timeout=min(timeout, 5))
        except subprocess.TimeoutExpired:
            try:
                proc.kill()
            except OSError:
                pass
            returncode = proc.wait(timeout=5)
        try:
            stderr_text = redact_text(
                proc.stderr.read().decode("utf-8", errors="replace").strip(),
                redacted_secrets,
            )
        except OSError:
            stderr_text = ""
        for stream in (proc.stdout, proc.stderr):
            try:
                stream.close()
            except OSError:
                pass

    if timed_out.is_set():
        detail = f": {stderr_text}" if stderr_text else ""
        raise SystemExit(
            f"supervisor_http {method} {path} on 127.0.0.1:{port} timed out after {timeout}s{detail}"
        )
    if http_err is not None:
        err_summary = redact_text(f"{type(http_err).__name__}: {http_err}", redacted_secrets)
        detail = f"; stderr={stderr_text}" if stderr_text else ""
        raise SystemExit(
            f"supervisor_http {method} {path} on 127.0.0.1:{port} failed "
            f"(rc={returncode}): {err_summary}{detail}"
        )

    if not raw_body:
        return status, None
    decoded = raw_body.decode("utf-8", errors="replace")
    try:
        parsed: object = json.loads(decoded)
    except json.JSONDecodeError:
        parsed = redact_text(decoded, redacted_secrets)
    return status, parsed


def build_official_archive(executable_bytes: bytes, license_bytes: bytes) -> bytes:
    tar_buffer = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=tar_buffer, compresslevel=1, mtime=0) as gz:
        with tarfile.open(mode="w", fileobj=gz, format=tarfile.PAX_FORMAT) as tf:
            exec_info = tarfile.TarInfo(name="cli-proxy-api")
            exec_info.size = len(executable_bytes)
            exec_info.mode = 0o755
            exec_info.mtime = 0
            tf.addfile(exec_info, io.BytesIO(executable_bytes))

            lic_info = tarfile.TarInfo(name="LICENSE")
            lic_info.size = len(license_bytes)
            lic_info.mode = 0o444
            lic_info.mtime = 0
            tf.addfile(lic_info, io.BytesIO(license_bytes))
    return tar_buffer.getvalue()


def cmd_upload_pinned(argv: list[str]) -> None:
    if len(argv) != 3:
        raise SystemExit(
            "usage: runtime-v8-upload-stage.py upload-pinned "
            "<container_id> <bundled_hold_path> <runtime_root>"
        )
    container_id, bundled_hold_path, runtime_root = argv
    token = read_container_runtime_token(container_id)
    manifest_raw = run_checked(["docker", "exec", container_id, "cat", MANIFEST_PATH]).stdout
    manifest = json.loads(manifest_raw.decode("utf-8"))
    if (
        manifest.get("schemaVersion") != 1
        or manifest.get("engine") != "cpa"
        or manifest.get("version") != EXPECTED_CPA_VERSION
    ):
        raise SystemExit(f"unexpected pinned CPA manifest: {manifest!r}")
    artifact_id = manifest.get("artifactId", "")
    if not isinstance(artifact_id, str) or not ARTIFACT_ID_RE.match(artifact_id):
        raise SystemExit(f"invalid pinned CPA artifactId: {artifact_id!r}")

    with tempfile.TemporaryDirectory(prefix="cpamp-v8-stage-upload-") as tmp_dir:
        tmp_path = pathlib.Path(tmp_dir)
        exec_file = tmp_path / "cli-proxy-api"
        license_file = tmp_path / "LICENSE"
        run_checked(["docker", "cp", f"{container_id}:{bundled_hold_path}", str(exec_file)])
        run_checked(["docker", "cp", f"{container_id}:{LICENSE_PATH}", str(license_file)])

        executable_bytes = exec_file.read_bytes()
        license_bytes = license_file.read_bytes()

    actual_exec_digest = "sha256:" + hashlib.sha256(executable_bytes).hexdigest()
    if actual_exec_digest != artifact_id:
        raise SystemExit(
            f"bundled CPA executable digest {actual_exec_digest} does not match manifest {artifact_id}"
        )
    if not license_bytes:
        raise SystemExit("bundled CPA LICENSE is empty")

    archive_bytes = build_official_archive(executable_bytes, license_bytes)
    expected_archive_digest = "sha256:" + hashlib.sha256(archive_bytes).hexdigest()

    pre_status, pre_body = supervisor_http(container_id, token, "GET", "/v1/runtime/artifact")
    if pre_status != 200 or not isinstance(pre_body, dict):
        safe_pre = redact_text(repr(pre_body), (token,))
        raise SystemExit(f"unexpected pre-upload artifact status: {pre_status} {safe_pre}")
    if pre_body.get("required") is not True or pre_body.get("active") is not None:
        safe_pre = redact_text(repr(pre_body), (token,))
        raise SystemExit(f"expected unbundled acquisition-required state before upload: {safe_pre}")
    if pre_body.get("pinned") != {"version": EXPECTED_CPA_VERSION, "artifactId": artifact_id}:
        safe_pre = redact_text(repr(pre_body), (token,))
        raise SystemExit(f"unexpected pinned status before upload: {safe_pre}")

    up_status, up_body = supervisor_http(
        container_id,
        token,
        "PUT",
        "/v1/runtime/artifact/upload",
        body=archive_bytes,
        content_type="application/gzip",
    )
    if up_status != 200 or not isinstance(up_body, dict):
        safe_up = redact_text(repr(up_body), (token,))
        raise SystemExit(f"PUT /v1/runtime/artifact/upload failed: {up_status} {safe_up}")
    operation = up_body.get("operation")
    if not isinstance(operation, dict) or operation.get("phase") != "succeeded" or operation.get("source") != "upload":
        safe_up = redact_text(repr(up_body), (token,))
        raise SystemExit(f"unexpected upload operation payload: {safe_up}")

    post_status, post_body = supervisor_http(container_id, token, "GET", "/v1/runtime/artifact")
    if post_status != 200 or not isinstance(post_body, dict):
        safe_post = redact_text(repr(post_body), (token,))
        raise SystemExit(f"unexpected post-upload artifact status: {post_status} {safe_post}")
    if post_body.get("required") is not False or post_body.get("active") != {
        "artifactId": artifact_id,
        "origin": "acquired",
    }:
        safe_post = redact_text(repr(post_body), (token,))
        raise SystemExit(f"unexpected post-upload active artifact status: {safe_post}")
    stage_meta_path = f"{runtime_root}/supervisor/artifacts/cpa/{EXPECTED_CPA_VERSION}/artifact.json"
    stage_meta_raw = run_checked(["docker", "exec", container_id, "cat", stage_meta_path]).stdout
    stage_meta = json.loads(stage_meta_raw.decode("utf-8"))
    expected_meta = {
        "schemaVersion": 1,
        "engine": "cpa",
        "version": EXPECTED_CPA_VERSION,
        "artifactId": artifact_id,
        "sourceArchiveDigest": expected_archive_digest,
    }
    if stage_meta != expected_meta:
        raise SystemExit(f"stage metadata mismatch: got {stage_meta!r}, want {expected_meta!r}")

    sys.stdout.write(artifact_id)


def cmd_post_json(argv: list[str]) -> None:
    usage = (
        "usage: runtime-v8-upload-stage.py post-json "
        "<container_id> <path> --body-file <0600_json_file> [--out-file <path>]"
    )
    if len(argv) < 4:
        raise SystemExit(usage)
    container_id = argv[0]
    path = argv[1]
    body_file: str | None = None
    out_file: str | None = None
    idx = 2
    while idx < len(argv):
        flag = argv[idx]
        if flag == "--body-file" and idx + 1 < len(argv) and body_file is None:
            body_file = argv[idx + 1]
            idx += 2
        elif flag == "--out-file" and idx + 1 < len(argv) and out_file is None:
            out_file = argv[idx + 1]
            idx += 2
        else:
            raise SystemExit(usage)
    if body_file is None:
        raise SystemExit(usage)

    raw_payload = read_private_file(body_file, "JSON body file")
    try:
        parsed_payload = json.loads(raw_payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as err:
        raise SystemExit(f"invalid JSON in --body-file {body_file!r}: {type(err).__name__}") from None
    body_secrets = tuple(collect_json_secrets(parsed_payload))
    token = read_container_runtime_token(container_id)
    status, body = supervisor_http(
        container_id,
        token,
        "POST",
        path,
        body=raw_payload,
        content_type="application/json",
        secrets_to_redact=body_secrets,
    )
    write_output_json(out_file, {"status": status, "body": body})


def cmd_cpa_get(argv: list[str]) -> None:
    usage = (
        "usage: runtime-v8-upload-stage.py cpa-get "
        "<container_id> <path> [--bearer-file <0600_token_file>] [--out-file <path>]"
    )
    if len(argv) < 2:
        raise SystemExit(usage)
    container_id = argv[0]
    path = argv[1]
    bearer_file: str | None = None
    out_file: str | None = None
    idx = 2
    while idx < len(argv):
        flag = argv[idx]
        if flag == "--bearer-file" and idx + 1 < len(argv) and bearer_file is None:
            bearer_file = argv[idx + 1]
            idx += 2
        elif flag == "--out-file" and idx + 1 < len(argv) and out_file is None:
            out_file = argv[idx + 1]
            idx += 2
        else:
            raise SystemExit(usage)

    bearer_token: str | None = None
    if bearer_file is not None:
        raw_token = read_private_file(bearer_file, "Bearer token file")
        bearer_token = raw_token.decode("utf-8", errors="replace").strip()
        if not bearer_token or any(ch.isspace() for ch in bearer_token):
            raise SystemExit(f"invalid Bearer token in {bearer_file!r}")

    status, body = supervisor_http(
        container_id,
        bearer_token,
        "GET",
        path,
        timeout=30,
        port=8317,
        include_runtime_features=False,
    )
    write_output_json(out_file, {"status": status, "body": body})


def main() -> None:
    if len(sys.argv) < 2:
        raise SystemExit("usage: runtime-v8-upload-stage.py <upload-pinned|post-json|cpa-get> ...")
    subcommand = sys.argv[1]
    if subcommand == "upload-pinned":
        cmd_upload_pinned(sys.argv[2:])
    elif subcommand == "post-json":
        cmd_post_json(sys.argv[2:])
    elif subcommand == "cpa-get":
        cmd_cpa_get(sys.argv[2:])
    else:
        raise SystemExit(f"unknown subcommand: {subcommand!r}")

if __name__ == "__main__":
    main()
