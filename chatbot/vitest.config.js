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
        all: true,
        reporter: ["text-summary", "text", "json-summary", "json"],
        reportsDirectory: "coverage",
        thresholds: { lines: 85 },
      },
    },
  }),
);
