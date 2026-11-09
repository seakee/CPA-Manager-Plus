"""Offline synthetic OCI fixtures. Never changes the production module or its pin.

A disposable module snapshot has a fixed fixture pin and no CLI entry point. Only
this test runner calls its exported functions; public CLI rejection tests always
use the untouched production scripts.
"""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from unittest.mock import patch

REPO = Path(__file__).resolve().parents[1]
OFFICIAL_SHA = "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"


def stage_renderer(root, fixture_sha):
    root = Path(root)
    root.mkdir(parents=True, exist_ok=True)
    source = (REPO / "bin/release/render-embedded-compose.mjs").read_text()
    # Test-only snapshot: no mutable pin, setter, production runCli, or CLI dispatch.
    source = source.split("export function runCli(", 1)[0]
    source = source.replace(OFFICIAL_SHA, fixture_sha)
    source = source.replace(
        "const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');",
        "const repoRoot = " + json.dumps(str(REPO)) + ";",
    )
    module = root / "synthetic-renderer.mjs"
    module.write_text(source)
    runner = root / "synthetic-runner.mjs"
    runner.write_text("""import { readFileSync, writeFileSync } from 'node:fs';
import { renderEmbeddedReleaseArtifacts, verifyEmbeddedReleaseArtifacts } from './synthetic-renderer.mjs';
const args = process.argv.slice(2);
const get = (flag) => args.includes(flag) ? args[args.indexOf(flag) + 1] : undefined;
try {
  const evidencePath = get('--evidence');
  const evidence = JSON.parse(readFileSync(evidencePath));
  const { dirname, join } = await import('node:path');
  const evidenceDir = dirname(evidencePath);
  const roleOciPaths = Object.fromEntries(['slim', 'full'].map(role =>
    [role, get('--' + role + '-oci') ?? join(evidenceDir, role + '.oci.tar')]));
  if (args.includes('--verify')) {
    const result = verifyEmbeddedReleaseArtifacts({ evidence, evidenceDir, roleOciPaths,
      composeText: readFileSync(get('--compose'), 'utf8'),
      pairing: JSON.parse(readFileSync(get('--pairing'))) });
    if (get('--compose-json-out')) writeFileSync(get('--compose-json-out'), JSON.stringify(result.composeDoc));
  } else {
    const result = renderEmbeddedReleaseArtifacts(evidence, { evidenceDir, roleOciPaths, version: get('--version') });
    writeFileSync(get('--compose-out'), result.composeYaml);
    writeFileSync(get('--pairing-out'), JSON.stringify(result.pairing, null, 2) + '\\n');
    if (get('--compose-json-out')) writeFileSync(get('--compose-json-out'), JSON.stringify(result.composeJson));
  }
} catch (error) {
  console.error(error.message);
  process.exit(1);
}
""")
    return module, runner


def redirect_fixture_node(subject, runner):
    """Route only the Python unit test's Node subprocess to the isolated fixture."""
    real_run = subprocess.run
    renderer = str(Path(subject.__file__).with_name("render-embedded-compose.mjs"))

    def run(cmd, *args, **kwargs):
        if len(cmd) > 1 and cmd[:2] == ["node", renderer]:
            cmd = ["node", str(runner), *cmd[2:]]
        kwargs.setdefault("cwd", str(REPO))
        return real_run(cmd, *args, **kwargs)

    return patch.object(subject.subprocess, "run", side_effect=run)


def create_archives(root):
    # Reuse the behavioral Python fixtures, including genuine compiled Go ELF metadata.
    import embedded_candidate_contract_test as fixtures
    fixtures.EmbeddedCandidateContracts.setUpClass()
    try:
        license_bytes = b"MIT License\n"
        root = Path(root)
        root.mkdir(parents=True, exist_ok=True)
        (root / "LICENSE").write_bytes(license_bytes)
        files = {
            "docker/runtime/config.v8.default.seed.yaml": b"seed",
            "docker/runtime/v8-entrypoint.sh": fixtures.MOCK_RUNTIME_ENTRYPOINT,
            "bin/release/embedded-third-party-inventory.txt": fixtures.SOURCE_BYTES,
            "tests/fixtures/r1-embedded-api-transport.json": (
                REPO / "tests/fixtures/r1-embedded-api-transport.json").read_bytes(),
        }
        for name, content in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
        fixture_sha = hashlib.sha256(fixtures.ROLE_BINARIES["ingress"]).hexdigest()
        api = files["tests/fixtures/r1-embedded-api-transport.json"]
        archives = {}
        for role in fixtures.subject.ROLES:
            archives[role] = root / (role + ".oci.tar")
            fixtures.make_oci(archives[role], role, license_bytes, api_contract=api)
        with patch.object(fixtures.subject, "CPA_SHA", fixture_sha), patch.object(
                fixtures.subject, "CPA_LICENSE_SHA", hashlib.sha256(b"official").hexdigest()):
            receipt = fixtures.subject.verify(archives, fixtures.SHA, fixtures.EVENT, root)
        (root / "candidate-evidence.json").write_text(json.dumps(receipt))
        module, runner = stage_renderer(root, fixture_sha)
        return {"module": str(module), "runner": str(runner), "sha": fixture_sha}
    finally:
        fixtures.EmbeddedCandidateContracts.tearDownClass()


if __name__ == "__main__":
    print(json.dumps(create_archives(Path(sys.argv[1]))))
