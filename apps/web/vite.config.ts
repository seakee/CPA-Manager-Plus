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
    const gitTag = execSync('git describe --tags --exact-match 2>/dev/null || git describe --tags 2>/dev/null || echo ""', { encoding: 'utf8' }).trim();
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

const isDemoSiteBuild = (mode: string) =>
  mode === 'demo' || process.env.DEMO_SITE === 'true' || process.env.VITE_DEMO_SITE === 'true';

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  const demoSite = isDemoSiteBuild(mode);
  const useRealDemoFixtures = demoSite || mode === 'test';

  return {
    plugins: [
      react(),
      viteSingleFile({
        removeViteModuleLoader: true
      })
    ],
    define: {
      __APP_VERSION__: JSON.stringify(getVersion()),
      __DEMO_SITE__: JSON.stringify(demoSite || mode === 'test')
    },
    server: {
      // 开发时把 CPA 管理接口转发给本地后端，面板用自身地址即可，无需另外配置 API 地址。
      // 默认指向 CPA 的 8317 端口，可用环境变量 VITE_CPA_DEV_TARGET 覆盖。
      proxy: {
        '/v0': process.env.VITE_CPA_DEV_TARGET || 'http://127.0.0.1:8317'
      }
    },
    resolve: {
      alias: [
        {
          find: /^@\/features\/demo\/demoFixtures$/,
          replacement: path.resolve(
            __dirname,
            useRealDemoFixtures
              ? './src/features/demo/demoFixtures.ts'
              : './src/features/demo/demoFixtures.empty.ts'
          )
        },
        {
          find: '@',
          replacement: path.resolve(__dirname, './src')
        }
      ]
    },
    css: {
      modules: {
        localsConvention: 'camelCase',
        generateScopedName: '[name]__[local]___[hash:base64:5]'
      },
      preprocessorOptions: {
        scss: {
          additionalData: `@use "@/styles/variables" as *;\n@use "@/styles/mixins" as *;\n`
        }
      }
    },
    build: {
      target: 'es2020',
      outDir: demoSite ? 'dist-demo' : 'dist',
      assetsInlineLimit: 100000000,
      chunkSizeWarningLimit: 100000000,
      cssCodeSplit: false,
      rolldownOptions: {
        output: {
          codeSplitting: false
        }
      }
    }
  };
});
