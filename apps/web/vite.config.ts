import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { viteSingleFile } from 'vite-plugin-singlefile';
import path from 'path';
import { execSync } from 'child_process';
import fs from 'fs';

// Get version from environment, git tag, or package.json
function getVersion(): string {
  // 1. Environment variable (set by GitHub Actions)
  if (process.env.VERSION) {
    return process.env.VERSION;
  }

  // 2. Try git tag
  try {
    const gitTag = execSync(
      'git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo ""',
      { encoding: 'utf8' }
    ).trim();
    if (gitTag) {
      return gitTag;
    }
  } catch {
    // Git not available or no tags
  }

  // 3. Fall back to package.json version
  try {
    const pkg = JSON.parse(fs.readFileSync(path.resolve(__dirname, 'package.json'), 'utf8'));
    if (pkg.version && pkg.version !== '0.0.0') {
      return pkg.version;
    }
  } catch {
    // package.json not readable
  }

  return 'dev';
}

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  const panelBuild = mode !== 'test';

  return {
    plugins: [
      react(),
      viteSingleFile({
        removeViteModuleLoader: true,
      }),
      ...(panelBuild
        ? [
            {
              name: 'panel-lite-artifact',
              apply: 'build' as const,
              closeBundle() {
                fs.renameSync(
                  path.resolve(__dirname, 'dist/panel/index.html'),
                  path.resolve(__dirname, 'dist/panel/management.html')
                );
              },
            },
          ]
        : []),
    ],
    define: {
      __PANEL_LITE__: JSON.stringify(panelBuild),
      __APP_VERSION__: JSON.stringify(getVersion()),
    },
    resolve: {
      alias: [
        ...(panelBuild
          ? [
              {
                find: /^(?:@\/services\/api\/usageService|\.\/usageService|.*\/services\/api\/usageService(?:\.ts)?)$/,
                replacement: path.resolve(__dirname, './src/panel/unsupportedManagerApi.ts'),
              },
              {
                find: /^(?:@\/fullweb\/publicApi|\.\/publicApi|.*\/fullweb\/publicApi(?:\.ts)?)$/,
                replacement: path.resolve(__dirname, './src/panel/unsupportedPublicApi.ts'),
              },
            ]
          : []),
        {
          find: '@',
          replacement: path.resolve(__dirname, './src'),
        },
      ],
    },
    css: {
      modules: {
        localsConvention: 'camelCase',
        generateScopedName: '[name]__[local]___[hash:base64:5]',
      },
      preprocessorOptions: {
        scss: {
          additionalData: `@use "@/styles/variables" as *;\n@use "@/styles/mixins" as *;\n`,
        },
      },
    },
    build: {
      target: 'es2020',
      outDir: 'dist/panel',
      assetsInlineLimit: 100000000,
      chunkSizeWarningLimit: 100000000,
      cssCodeSplit: false,
      rolldownOptions: {
        output: {
          codeSplitting: false,
        },
      },
    },
  };
});
