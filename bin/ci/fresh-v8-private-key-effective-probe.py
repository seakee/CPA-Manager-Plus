#!/usr/bin/env python3
"""Destructive, private-only CPA v8.0.55 Client Key effect experiment.

Run ONLY inside the trusted namespace of a fresh, disposable #1008 proof
profile, after verifying the official CPA binary and empty synthetic volumes.
This tool never opens the CPAMP Gateway and never declares runtimeEffective.
"""

import argparse
import datetime
import http.client
import json
import os
import secrets
import sys
import time


class StopProof(Exception):
    pass


def request(port, method, path, authorization=None, payload=None):
    headers = {"Accept": "application/json", "Cache-Control": "no-store"}
    if authorization:
        headers["Authorization"] = "Bearer " + authorization
    body = None
    if payload is not None:
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        headers["Content-Type"] = "application/json"
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
    try:
        connection.request(method, path, body=body, headers=headers)
        response = connection.getresponse()
        data = response.read(1 << 20)
        if response.read(1):
            raise StopProof("HTTP response exceeded evidence limit")
        return response.status, data
    except (OSError, http.client.HTTPException) as exc:
        # Never echo exception/response text: it may contain credentials.
        raise StopProof("trusted loopback HTTP exchange failed; no retry") from exc
    finally:
        connection.close()


def event(evidence, **fields):
    evidence["events"].append({
        "utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "elapsedSeconds": round(time.monotonic() - evidence["_started"], 6),
        **fields,
    })


def json_value(port, path, management_key, evidence, phase):
    status, data = request(port, "GET", path, management_key)
    event(evidence, phase=phase, action="config-get", path=path, status=status)
    if status != 200:
        raise StopProof("Management safe read rejected: HTTP " + str(status))
    try:
        return json.loads(data)
    except (ValueError, UnicodeError) as exc:
        raise StopProof("Management returned invalid JSON") from exc


def check_ingress(port, phase, evidence):
    for mode in ("absent", "wrong", "valid"):
        # The public Gateway MUST stay Pending irrespective of the client key.
        status, _ = request(port, "GET", "/v1/models",
                            {"absent": None, "wrong": "synthetic-invalid",
                             "valid": evidence["_latest_secret"]}[mode])
        event(evidence, phase=phase, edge="ingress", credential=mode, status=status)
        if status != 503:
            raise StopProof("Ingress forwarded or changed Pending at " + phase)


def check_cpa(port, phase, tokens, evidence):
    statuses = {}
    for name, token in tokens.items():
        status, _ = request(port, "GET", "/v1/models", token)
        event(evidence, phase=phase, edge="private-cpa", credential=name, status=status)
        statuses[name] = status
    return statuses


def set_only_client_keys(port, management_key, values, previous, phase, evidence):
    stored = json_value(port, "/v8/management/config/access/api-keys",
                        management_key, evidence, phase + "-before-write")
    if stored != previous:
        raise StopProof("Client Key ownership changed before " + phase)
    # No retries: a timeout leaves write state unknown and the experiment STOPS.
    event(evidence, phase=phase, action="config-put-attempt", keyCount=len(values))
    status, _ = request(port, "PUT", "/v8/management/config/access/api-keys",
                        management_key, values)
    event(evidence, phase=phase, action="config-put", status=status, keyCount=len(values))
    if status != 200:
        raise StopProof("Official CPA Client Key write failed at " + phase)
    stored = json_value(port, "/v8/management/config/access/api-keys",
                        management_key, evidence, phase)
    if stored != values:
        raise StopProof("Management configuration readback mismatch at " + phase)
    event(evidence, phase=phase, action="readback-matched", keyCount=len(values))
    # Readback is not an effective-provider acknowledgment.


def await_auth(port, phase, expected, previous, tokens, ingress_port, evidence, deadline):
    end = time.monotonic() + deadline
    stable_since = None
    samples = 0
    reached = set()
    while True:
        if time.monotonic() >= end:
            raise StopProof("Live CPA auth stability timed out at " + phase)
        check_ingress(ingress_port, phase, evidence)
        observed = check_cpa(port, phase, tokens, evidence)
        now = time.monotonic()
        event(evidence, phase=phase, action="auth-sweep", statuses=observed,
              observation="candidate" if observed == expected else "transition")
        if now >= end:
            raise StopProof("Live CPA auth stability timed out at " + phase)
        for name, status in observed.items():
            if status not in (previous[name], expected[name]):
                raise StopProof("Unexpected CPA authentication status at " + phase)
            if name in reached and status != expected[name]:
                raise StopProof("CPA authentication regressed at " + phase)
            if status == expected[name]:
                reached.add(name)
        if observed == expected:
            if stable_since is None:
                stable_since = now
            samples += 1
            # A sequence is not an atomic snapshot. Require both elapsed time
            # and repeated sweeps; this only proves the bounded sampled window.
            if now - stable_since >= 2 and samples >= 5:
                event(evidence, phase=phase, action="stable-window",
                      durationSeconds=round(now - stable_since, 6), sweeps=samples)
                return
        time.sleep(0.25)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa-port", type=int, default=8317)
    parser.add_argument("--ingress-port", type=int, default=28471)
    parser.add_argument("--reload-seconds", type=float, default=15)
    args = parser.parse_args()
    if (os.environ.get("CPAMP_PRIVATE_CPA_KEY_PROOF") != "1" or
            os.environ.get("CPAMP_PROOF_DISPOSABLE_VOLUME") != "1"):
        raise StopProof("Disposable proof must be explicitly attested in environment")
    if (args.cpa_port != 8317 or not 1 <= args.ingress_port <= 65535 or
            args.ingress_port == args.cpa_port or not 3 <= args.reload_seconds <= 60):
        raise StopProof("Unsupported proof ports or reload budget")
    management = os.environ.get("CPAMP_PROOF_CPA_MANAGEMENT_KEY", "")
    if len(management) < 32 or any(not 33 <= ord(ch) <= 126 for ch in management):
        raise StopProof("Missing private synthetic Management credential")

    evidence = {"result": "STOP", "binaryVerification": "OPERATOR_PREFLIGHT_REQUIRED",
                "gatewayAuthorized": False, "runtimeEffective": False,
                "events": [], "_latest_secret": None, "_started": time.monotonic()}
    try:
        run(args, management, evidence)
    except StopProof as exc:
        evidence["reason"] = str(exc)
        raise
    finally:
        # No automated rollback/retry after an uncertain mutation.
        evidence.pop("_latest_secret", None)
        evidence.pop("_started", None)
        print(json.dumps(evidence, sort_keys=True), flush=True)


def run(args, management, evidence):
    cfg = json_value(args.cpa_port, "/v8/management/config", management, evidence, "preflight")
    if not isinstance(cfg, dict):
        raise StopProof("Management config projection is not an object")
    server = cfg.get("server", {})
    management_cfg = cfg.get("management", {})
    plugins = cfg.get("plugins", {})
    oauth = cfg.get("oauth", {})
    # The exact empty seed is the only writable experiment target. Official v8
    # accepts legacy provider aliases too; checking a few canonical fields does
    # not exclude real credentials. No synthetic Provider exception is implied.
    if (set(cfg) - {"config-version", "server", "management", "access", "oauth",
                    "plugins", "observability"} or
            oauth != {"auth-dir": "/runtime/gateway/auth"} or
            plugins != {"enabled": False, "dir": "/runtime/gateway/plugins"} or
            cfg.get("access") != {"api-keys": []} or
            cfg.get("observability") != {
                "logs": {"logging-to-file": False},
                "usage": {"usage-statistics-enabled": False}}):
        raise StopProof("Configuration differs from the empty synthetic proof seed")
    if (cfg.get("config-version") != 8 or
            server != {"host": "127.0.0.1", "port": 8317} or
            not isinstance(management_cfg, dict) or
            set(management_cfg) != {"allow-remote", "secret-key", "disable-control-panel",
                                    "disable-auto-update-panel"} or
            management_cfg.get("allow-remote") is not False or
            management_cfg.get("disable-control-panel") is not True or
            management_cfg.get("disable-auto-update-panel") is not True):
        raise StopProof("Management view is not the constrained loopback CPA seed")
    if json_value(args.cpa_port, "/v8/management/config/access/api-keys",
                  management, evidence, "preflight") != []:
        raise StopProof("Refusing to touch nonempty Client Key configuration")

    first = "cpamp_sk_" + secrets.token_hex(32)
    second = "cpamp_sk_" + secrets.token_hex(32)
    evidence["_latest_secret"] = first
    tokens = {"absent": None, "wrong": "synthetic-invalid", "valid": first}
    empty = {name: 200 for name in tokens}
    # EMPTY official CPA provider list allows anonymous requests, not CPAMP grants.
    await_auth(args.cpa_port, "empty", empty, empty, tokens,
               args.ingress_port, evidence, args.reload_seconds)

    set_only_client_keys(args.cpa_port, management, [first], [], "install", evidence)
    await_auth(args.cpa_port, "install",
               {"absent": 401, "wrong": 401, "valid": 200}, empty, tokens,
               args.ingress_port, evidence, args.reload_seconds)

    set_only_client_keys(args.cpa_port, management, [second], [first], "replace", evidence)
    evidence["_latest_secret"] = second
    tokens = {"old": first, "new": second, "absent": None, "wrong": "synthetic-invalid"}
    replaced = {"old": 401, "new": 200, "absent": 401, "wrong": 401}
    await_auth(args.cpa_port, "replace", replaced,
               {"old": 200, "new": 401, "absent": 401, "wrong": 401}, tokens,
               args.ingress_port, evidence, args.reload_seconds)

    set_only_client_keys(args.cpa_port, management, [], [second], "clear-last", evidence)
    await_auth(args.cpa_port, "clear-last", {name: 200 for name in tokens}, replaced,
               tokens, args.ingress_port, evidence, args.reload_seconds)
    evidence["result"] = "PASS_WITH_EXPLICIT_STOPS"


if __name__ == "__main__":
    try:
        main()
    except StopProof as exc:
        print(json.dumps({"result": "STOP", "reason": str(exc),
                          "gatewayAuthorized": False,
                          "runtimeEffective": False}), file=sys.stderr)
        sys.exit(1)
