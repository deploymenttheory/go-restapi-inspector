import { defineConfig } from "playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.js",
  fullyParallel: true,
  timeout: 30000,
  reporter: "list",
  use: { browserName: "chromium", viewport: { width: 1440, height: 1000 }, colorScheme: "light", trace: "retain-on-failure" },
});
