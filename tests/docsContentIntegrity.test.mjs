import { existsSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const docsRoot = path.join(repoRoot, 'apps/docs');
const configPath = path.join(docsRoot, '.vitepress/config.ts');

const contentRoots = [
  'guide',
  'gateway',
  'manual',
  'deployment',
  'operations',
  'migration',
  'troubleshooting',
  'reference',
];

const markdownFiles = (root) => {
  const result = [];
  const visit = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const absolutePath = path.join(directory, entry.name);
      if (entry.isDirectory()) visit(absolutePath);
      else if (entry.isFile() && entry.name.endsWith('.md')) result.push(absolutePath);
    }
  };
  visit(root);
  return result;
};

const relativeMarkdownPaths = (root) =>
  markdownFiles(root).map((filePath) => path.relative(root, filePath).replaceAll(path.sep, '/'));

const routeToMarkdownPath = (route) => {
  if (route === '/') return path.join(docsRoot, 'index.md');
  if (route === '/en/' || route === '/en') return path.join(docsRoot, 'en/index.md');
  return path.join(docsRoot, `${route.replace(/^\//, '')}.md`);
};

describe('documentation content integrity', () => {
  it('keeps Chinese and English documentation page sets aligned', () => {
    const chinese = contentRoots.flatMap((root) =>
      relativeMarkdownPaths(path.join(docsRoot, root)).map(
        (relativePath) => `${root}/${relativePath}`
      )
    );
    const english = contentRoots.flatMap((root) =>
      relativeMarkdownPaths(path.join(docsRoot, 'en', root)).map(
        (relativePath) => `${root}/${relativePath}`
      )
    );

    expect(new Set(chinese)).toEqual(new Set(english));
  });

  it('keeps local VitePress navigation and sidebar links resolvable', () => {
    const config = readFileSync(configPath, 'utf8');
    const routes = [...config.matchAll(/link:\s*'([^']+)'/g)]
      .map((match) => match[1])
      .filter((route) => route.startsWith('/'));
    const missing = [...new Set(routes)].filter((route) => !existsSync(routeToMarkdownPath(route)));

    expect(missing).toEqual([]);
  });

  it('requires title and description frontmatter on discovery-critical pages', () => {
    const criticalPages = [
      'index.md',
      'en/index.md',
      'guide/choosing-a-panel.md',
      'en/guide/choosing-a-panel.md',
      'deployment/cpa-panel.md',
      'en/deployment/cpa-panel.md',
      'reference/capability-matrix.md',
      'en/reference/capability-matrix.md',
      'manual/ai-providers.md',
      'en/manual/ai-providers.md',
      'manual/codex-inspection.md',
      'en/manual/codex-inspection.md',
      'manual/monitoring.md',
      'en/manual/monitoring.md',
      'manual/usage-analytics.md',
      'en/manual/usage-analytics.md',
    ];

    for (const relativePath of criticalPages) {
      const content = readFileSync(path.join(docsRoot, relativePath), 'utf8');
      expect(content, `${relativePath} title`).toMatch(/^---[\s\S]*?\ntitle:\s*.+/);
      expect(content, `${relativePath} description`).toMatch(/^---[\s\S]*?\ndescription:\s*.+/);
    }
  });

  it('keeps README discovery language and primary docs entry points', () => {
    const readme = readFileSync(path.join(repoRoot, 'README.md'), 'utf8');
    const readmeZh = readFileSync(path.join(repoRoot, 'README_CN.md'), 'utf8');

    expect(readme).toContain('CPA / CLIProxyAPI management panel');
    expect(readme).toContain('Choosing A CPA Panel');
    expect(readme).toContain('CPAMP Lightweight Panel');
    expect(readme).toContain('Capability Matrix');
    expect(readmeZh).toContain('CPA / CLIProxyAPI 的自托管管理面板');
    expect(readmeZh).toContain('如何选择 CPA 面板');
    expect(readmeZh).toContain('CPAMP 轻量面板');
    expect(readmeZh).toContain('能力矩阵');
  });

  it('keeps lightweight-panel and Manager Server boundaries accurate', () => {
    const docsContent = markdownFiles(docsRoot)
      .map((filePath) => readFileSync(filePath, 'utf8'))
      .join('\n');
    const readme = readFileSync(path.join(repoRoot, 'README.md'), 'utf8');
    const readmeZh = readFileSync(path.join(repoRoot, 'README_CN.md'), 'utf8');

    expect(readme).not.toContain('| CPA-hosted panel');
    expect(readmeZh).not.toContain('| CPA 托管面板');
    expect(docsContent).not.toContain('After connecting Manager Server');
    expect(docsContent).not.toContain('连接 Manager Server 后');
    expect(docsContent).not.toContain('full usage features still need Manager Server');
    expect(docsContent).not.toContain('完整用量能力仍需 Manager Server');
    expect(docsContent).not.toContain('remote-management.panel-repo');
    expect(docsContent).toContain('panel-github-repository');
  });

  it('separates runnable product modes from the demo preview', () => {
    const readme = readFileSync(path.join(repoRoot, 'README.md'), 'utf8');
    const readmeZh = readFileSync(path.join(repoRoot, 'README_CN.md'), 'utf8');
    const docsIndex = readFileSync(path.join(docsRoot, 'index.md'), 'utf8');
    const docsIndexEn = readFileSync(path.join(docsRoot, 'en/index.md'), 'utf8');
    const choosingPanel = readFileSync(path.join(docsRoot, 'guide/choosing-a-panel.md'), 'utf8');
    const choosingPanelEn = readFileSync(
      path.join(docsRoot, 'en/guide/choosing-a-panel.md'),
      'utf8'
    );
    const capabilityMatrix = readFileSync(
      path.join(docsRoot, 'reference/capability-matrix.md'),
      'utf8'
    );
    const capabilityMatrixEn = readFileSync(
      path.join(docsRoot, 'en/reference/capability-matrix.md'),
      'utf8'
    );

    expect(readme).toContain('| CPAMP Lightweight Panel');
    expect(readme).toContain('| CPAMP Full Mode');
    expect(readme).not.toContain('| Live Demo');
    expect(readme).toContain('It is not a deployment or runtime mode');
    expect(readme).not.toContain('| Native Manager Server');
    expect(readmeZh).toContain('| CPAMP 轻量面板');
    expect(readmeZh).toContain('| CPAMP 完整模式');
    expect(readmeZh).not.toContain('| 在线演示');
    expect(readmeZh).toContain('不是部署或运行模式');
    expect(readmeZh).not.toContain('| Full Docker');
    expect(docsIndex.split('## 先体验界面')[0]).not.toContain('<h3>在线演示</h3>');
    expect(docsIndex).toContain('不是部署或运行模式');
    expect(docsIndexEn.split('## Preview The Interface')[0]).not.toContain('<h3>Live Demo</h3>');
    expect(docsIndexEn).toContain('not a deployment or runtime mode');
    expect(choosingPanel).not.toContain('| 在线演示');
    expect(choosingPanel).toContain('演示站不是面板、部署方式或运行模式');
    expect(choosingPanelEn).not.toContain('| Live Demo');
    expect(choosingPanelEn).toContain('It is not a panel, deployment option, or runtime mode');
    expect(capabilityMatrix).toContain('CPAMP 轻量面板');
    expect(capabilityMatrix).toContain('CPAMP 完整模式');
    expect(capabilityMatrix).not.toContain('| 在线演示');
    expect(capabilityMatrix).toContain('在线演示不是运行模式');
    expect(capabilityMatrix).not.toContain('| Full Docker');
    expect(capabilityMatrix).not.toContain('| 原生 Manager Server');
    expect(capabilityMatrixEn).toContain('CPAMP Lightweight Panel');
    expect(capabilityMatrixEn).toContain('CPAMP Full Mode');
    expect(capabilityMatrixEn).not.toContain('| Live Demo');
    expect(capabilityMatrixEn).toContain('The Live Demo is not a runtime mode');
    expect(capabilityMatrixEn).not.toContain('| Native Manager Server');
  });

  it('keeps primary documentation user-focused and progressive', () => {
    const config = readFileSync(configPath, 'utf8');
    const gettingStarted = readFileSync(path.join(docsRoot, 'guide/getting-started.md'), 'utf8');
    const gettingStartedEn = readFileSync(
      path.join(docsRoot, 'en/guide/getting-started.md'),
      'utf8'
    );
    const dashboard = readFileSync(path.join(docsRoot, 'manual/dashboard.md'), 'utf8');
    const dashboardEn = readFileSync(path.join(docsRoot, 'en/manual/dashboard.md'), 'utf8');
    const troubleshooting = readFileSync(
      path.join(docsRoot, 'troubleshooting/request-monitoring.md'),
      'utf8'
    );
    const troubleshootingEn = readFileSync(
      path.join(docsRoot, 'en/troubleshooting/request-monitoring.md'),
      'utf8'
    );
    const releases = readFileSync(path.join(docsRoot, 'reference/releases.md'), 'utf8');
    const releasesEn = readFileSync(path.join(docsRoot, 'en/reference/releases.md'), 'utf8');

    expect(config).toContain("text: '开始使用'");
    expect(config).toContain("text: '日常使用'");
    expect(config).toContain("text: '维护与排障'");
    expect(config).toMatch(/text: '高级配置',\n\s+collapsed: true/);
    expect(config).toMatch(/text: '参考',\n\s+collapsed: true/);
    expect(config).toContain("text: 'Get Started'");
    expect(config).toMatch(/text: 'Advanced Configuration',\n\s+collapsed: true/);
    expect(config).not.toContain('性能优化报告（2026-07-10）');
    expect(config).not.toContain('Performance Report (2026-07-10)');

    expect(gettingStarted).toContain('路线一：安装轻量面板');
    expect(gettingStarted).toContain('路线二：安装完整模式');
    expect(gettingStarted).not.toMatch(/^services:/m);
    expect(gettingStartedEn).toContain('Path 1: Install Lightweight Panel');
    expect(gettingStartedEn).toContain('Path 2: Install Full Mode');
    expect(gettingStartedEn).not.toMatch(/^services:/m);

    expect(dashboard).not.toContain('checkpoint');
    expect(dashboardEn).not.toContain('hourly rollup');
    expect(troubleshooting.indexOf('## 按顺序检查')).toBeLessThan(
      troubleshooting.indexOf('高级诊断')
    );
    expect(troubleshootingEn.indexOf('## Check In This Order')).toBeLessThan(
      troubleshootingEn.indexOf('Advanced diagnostics')
    );

    expect(releases).not.toContain('docs/release-notes');
    expect(releases).not.toContain('Tag push');
    expect(releasesEn).not.toContain('docs/release-notes');
    expect(releasesEn).not.toContain('GitHub Actions');
  });
});
