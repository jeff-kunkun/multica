// @vitest-environment node
import { mkdir, mkdtemp, readFile, writeFile } from "fs/promises";
import { join } from "path";
import { tmpdir } from "os";
import { describe, expect, it } from "vitest";
import { DEFAULT_RUNTIME_CONFIG } from "../shared/runtime-config";
import { loadRuntimeConfig } from "./runtime-config-loader";

describe("loadRuntimeConfig", () => {
  it("uses dev env and ignores desktop.json during electron-vite dev", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://prod.example.com" }),
    );

    await expect(
      loadRuntimeConfig({
        isDev: true,
        configPath,
        env: {
          apiUrl: "http://localhost:8080",
          wsUrl: "ws://localhost:8080/ws",
          appUrl: "http://localhost:3000",
        },
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "http://localhost:8080",
        wsUrl: "ws://localhost:8080/ws",
        appUrl: "http://localhost:3000",
      },
    });
  });

  it("uses cloud defaults when packaged config is absent", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    await expect(
      loadRuntimeConfig({
        isDev: false,
        configPath: join(dir, "missing.json"),
        env: {},
      }),
    ).resolves.toEqual({
      ok: true,
      config: DEFAULT_RUNTIME_CONFIG,
    });
  });

  it("parses a valid packaged desktop.json", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.example.com" }),
    );

    await expect(
      loadRuntimeConfig({ isDev: false, configPath, env: {} }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://api.example.com",
        wsUrl: "wss://api.example.com/ws",
        appUrl: "https://example.com",
      },
    });
  });

  it("fails closed when packaged desktop.json is invalid", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(configPath, "{");

    const result = await loadRuntimeConfig({ isDev: false, configPath, env: {} });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error.message).toContain(configPath);
      expect(result.error.message).toContain("Invalid desktop runtime config JSON");
    }
  });

  async function homeWithLegacyConfig() {
    const home = await mkdtemp(join(tmpdir(), "multica-desktop-home-"));
    await mkdir(join(home, ".multica"));
    const legacyPath = join(home, ".multica", "desktop.json");
    const legacy = JSON.stringify({ schemaVersion: 1, apiUrl: "https://origin.ai.ferryway.cc" });
    await writeFile(legacyPath, legacy);
    return { home, legacyPath, legacy };
  }

  it("connects to the office when only the kun-line desktop.json exists", async () => {
    const { home } = await homeWithLegacyConfig();
    await expect(
      loadRuntimeConfig({ isDev: false, homeDir: home, env: {} }),
    ).resolves.toEqual({ ok: true, config: DEFAULT_RUNTIME_CONFIG });
  });

  it("applies the office's own office-desktop.json override", async () => {
    const { home } = await homeWithLegacyConfig();
    await writeFile(
      join(home, ".multica", "office-desktop.json"),
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://office.example.com" }),
    );
    const result = await loadRuntimeConfig({ isDev: false, homeDir: home, env: {} });
    expect(result.ok && result.config.apiUrl).toBe("https://office.example.com");
  });

  it("keeps the two apps' config files independent", async () => {
    const { home, legacyPath, legacy } = await homeWithLegacyConfig();
    const officePath = join(home, ".multica", "office-desktop.json");
    await writeFile(officePath, "{ not json");

    // A broken office file is reported against the office path, and the
    // kun-line file is neither read nor rewritten.
    const result = await loadRuntimeConfig({ isDev: false, homeDir: home, env: {} });
    expect(!result.ok && result.error.message).toContain(officePath);
    await expect(readFile(legacyPath, "utf-8")).resolves.toBe(legacy);
  });
});
