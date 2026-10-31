import { test } from 'vitest';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, copyFileSync, rmSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// Run the real packaging script against a tiny controlled source tree and fake
// toolchain. Assertions inspect generated package manifests, not script text.
test('native packages add GUI companion only to Windows amd64 and arm64', () => {
  const root = mkdtempSync(path.join(path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../bin'), 'monitor-package-test-'));
  try {
    for (const dir of ['bin/release', 'bin/native', 'apps/manager-server/internal/httpapi/web', 'apps/web/dist', 'docs', 'tools']) mkdirSync(path.join(root, dir), { recursive: true });
    copyFileSync(new URL('../bin/release/package-native.sh', import.meta.url), path.join(root, 'bin/release/package-native.sh'));
    for (const name of ['README.md', 'README_CN.md', 'LICENSE', 'bin/native/cpa-manager-plusctl.ps1', 'bin/native/cpa-manager-plusctl.sh', 'apps/web/dist/index.html', 'apps/web/dist/favicon.ico', 'apps/web/dist/apple-touch-icon.png']) writeFileSync(path.join(root, name), 'fixture\n');
    const script = (name, body) => writeFileSync(path.join(root, 'tools', name), `#!/usr/bin/env bash\nset -eu\n${body}\n`, { mode: 0o755 });
    script('git', 'printf fixture-commit');
    script('go', `output=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then shift; output="$1"; fi
  shift
done
printf '%s/%s' "$GOOS" "$GOARCH" > "$output"`);
    script('zip', 'ls "$3" > "$2"');
    script('tar', 'ls "$3" > "$2"');
    const slash = (value) => value.replaceAll('\\', '/').replace(/^([A-Za-z]):\//, (_, drive) => `/${drive.toLowerCase()}/`);
    // Prefix inside bash: Windows process PATH has case-insensitive aliases.
    execFileSync('bash', ['-c', `export PATH="${slash(path.join(root, 'tools'))}:$PATH"; bash "${slash(path.join(root, 'bin/release/package-native.sh'))}"`], { cwd: root, encoding: 'utf8', env: { ...process.env, VERSION: 'fixture' } });
    const output = path.join(root, 'dist/native');
    const archives = readdirSync(output).filter((name) => /\.(zip|gz)$/.test(name));
    assert.equal(archives.length, 6);
    for (const archive of archives) {
      const manifest = readFileSync(path.join(output, archive), 'utf8');
      assert.equal(manifest.includes('cpa-manager-plus-monitor.exe'), archive.includes('_windows_'), archive);
      assert.ok(manifest.includes(archive.includes('_windows_') ? 'cpa-manager-plus.exe' : 'cpa-manager-plus'));
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
