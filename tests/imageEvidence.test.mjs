import { execFileSync } from 'node:child_process';
import { it } from 'vitest';

it('collects original dependency notices and verifies byte-bound image provenance', () => {
  execFileSync('python3', ['tests/image_evidence_test.py'], {
    cwd: new URL('..', import.meta.url),
    env: {
      ...process.env,
      PYTHONDONTWRITEBYTECODE: '1',
      GOFLAGS: '-p=2',
      GOMAXPROCS: '2',
      GOPROXY: 'off',
      HTTP_ADDR: '127.0.0.1:0',
    },
    stdio: 'pipe',
    timeout: 90_000,
  });
}, 90_000);
