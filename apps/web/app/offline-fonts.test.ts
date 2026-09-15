// @vitest-environment node
import { readFileSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const layoutFiles = [
  "app/layout.tsx",
  "app/(landing)/layout.tsx",
];

const fontFiles = [
  "fonts/inter-latin-wght-normal.woff2",
  "fonts/inter-latin-wght-italic.woff2",
  "fonts/source-serif-4-latin-wght-normal.woff2",
  "fonts/source-serif-4-latin-wght-italic.woff2",
  "fonts/instrument-serif-latin-400-normal.woff2",
  "fonts/geist-mono-latin-wght-normal.woff2",
];

describe("offline web fonts", () => {
  it("loads layouts via next/font/local, not next/font/google", () => {
    for (const rel of layoutFiles) {
      const source = readFileSync(path.join(webRoot, rel), "utf8");
      expect(source, rel).toContain('from "next/font/local"');
      expect(source, rel).not.toMatch(/from\s+["']next\/font\/google["']/);
    }
  });

  it("keeps latin-subset woff2 files in apps/web/fonts", () => {
    for (const rel of fontFiles) {
      const stats = statSync(path.join(webRoot, rel));
      expect(stats.size, rel).toBeGreaterThan(1024);
    }
  });
});
