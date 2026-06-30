import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const bundlePath = path.resolve(repoRoot, process.argv[2] || 'apps/web/dist/index.html');

const forbiddenMarkers = [
  '#/demo',
  '/demo/oauth',
  'http://demo.local',
  'demo-management-key',
  'demo-cpa-management-key',
  'request-errors-',
  'hash_openai_primary',
  'codex-fallback-02',
  'request-insights',
  'demo-usage.sqlite',
  'demo-request-',
  'demo-event-',
  'demo-trace-',
];

if (!existsSync(bundlePath)) {
  console.error(`Missing web bundle: ${path.relative(repoRoot, bundlePath)}`);
  process.exit(1);
}

const bundle = readFileSync(bundlePath, 'utf8');
const hits = forbiddenMarkers.filter((marker) => bundle.includes(marker));

if (hits.length > 0) {
  console.error(
    [
      `Default web bundle contains demo-only markers: ${path.relative(repoRoot, bundlePath)}`,
      ...hits.map((marker) => `- ${marker}`),
    ].join('\n')
  );
  process.exit(1);
}

console.log(
  `Default web bundle is free of demo fixture markers: ${path.relative(repoRoot, bundlePath)}`
);
