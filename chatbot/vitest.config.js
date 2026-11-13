import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config.js";

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      include: ["tests/**/*.test.{mjs,js,jsx}"],
      setupFiles: ["tests/setup.js"],
      maxWorkers: 2,
      minWorkers: 1,
      testTimeout: 20000,
      coverage: {
        provider: "v8",
        include: ["src/**/*.{js,jsx}"],
        reporter: ["text-summary", "text", "json-summary", "json"],
        reportsDirectory: "coverage",
        // Vitest 4 remaps V8 coverage with an AST-aware mapper. The same
        // suite reports 81.86% lines here versus 87.39% under Vitest 3's
        // older, inflated mapping. Keep the gate at the new baseline.
        thresholds: { lines: 81 },
      },
    },
  }),
);
