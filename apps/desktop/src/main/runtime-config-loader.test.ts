// @vitest-environment node
import { access, mkdtemp, readFile, writeFile } from "fs/promises";
import { join } from "path";
import { tmpdir } from "os";
import { describe, expect, it, vi } from "vitest";
import { constants as fsConstants } from "fs";

vi.mock("electron", () => ({
  app: {
    getPath: () => "/tmp",
  },
}));

import { loadRuntimeConfig, switchRuntimeConfig } from "./runtime-config-loader";

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

  it("enters on the self-hosted instance when packaged config is absent", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    await expect(
      loadRuntimeConfig({
        isDev: false,
        configPath: join(dir, "missing.json"),
        env: {},
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://ai.ferryway.cc",
        wsUrl: "wss://ai.ferryway.cc/ws",
        appUrl: "https://ai.ferryway.cc",
      },
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
});

describe("switchRuntimeConfig", () => {
  async function configPath(): Promise<string> {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-switch-"));
    return join(dir, "desktop.json");
  }

  it("writes a self-hosted config and reads it back", async () => {
    const path = await configPath();
    const switched = await switchRuntimeConfig({
      configPath: path,
      target: { type: "url", url: "https://ai.ferryway.cc" },
    });

    expect(switched).toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://ai.ferryway.cc",
        wsUrl: "wss://ai.ferryway.cc/ws",
        appUrl: "https://ai.ferryway.cc",
      },
    });
    expect(JSON.parse(await readFile(path, "utf-8"))).toEqual({
      schemaVersion: 1,
      apiUrl: "https://ai.ferryway.cc",
      wsUrl: "wss://ai.ferryway.cc/ws",
      appUrl: "https://ai.ferryway.cc",
    });
    await expect(
      loadRuntimeConfig({ isDev: false, configPath: path, env: {} }),
    ).resolves.toEqual(switched);
  });

  it("rejects an invalid URL and leaves the original file unchanged", async () => {
    const path = await configPath();
    const original = JSON.stringify({
      schemaVersion: 1,
      apiUrl: "https://ai.ferryway.cc",
      wsUrl: "wss://ai.ferryway.cc/ws",
      appUrl: "https://ai.ferryway.cc",
    });
    await writeFile(path, original);

    const switched = await switchRuntimeConfig({
      configPath: path,
      target: { type: "url", url: "ftp://evil.example" },
    });

    expect(switched.ok).toBe(false);
    if (!switched.ok) {
      expect(switched.error).toMatch(/http or https/);
    }
    expect(await readFile(path, "utf-8")).toBe(original);
  });

  it("does not create a file when the URL is invalid and none existed", async () => {
    const path = await configPath();
    const switched = await switchRuntimeConfig({
      configPath: path,
      target: { type: "url", url: "not a url" },
    });

    expect(switched.ok).toBe(false);
    await expect(access(path, fsConstants.F_OK)).rejects.toMatchObject({
      code: "ENOENT",
    });
  });

  it("writes official cloud explicitly when switching back to it", async () => {
    const path = await configPath();
    await writeFile(
      path,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://ai.ferryway.cc" }),
    );

    const switched = await switchRuntimeConfig({
      configPath: path,
      target: { type: "official" },
    });

    expect(switched).toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://api.multica.ai",
        wsUrl: "wss://api.multica.ai/ws",
        appUrl: "https://multica.ai",
      },
    });
    // The absent-file fallback is the self-hosted entry default, so official
    // cloud has to survive as an explicit file rather than a deletion.
    expect(JSON.parse(await readFile(path, "utf-8"))).toEqual({
      schemaVersion: 1,
      apiUrl: "https://api.multica.ai",
      wsUrl: "wss://api.multica.ai/ws",
      appUrl: "https://multica.ai",
    });
    await expect(
      loadRuntimeConfig({ isDev: false, configPath: path, env: {} }),
    ).resolves.toEqual(switched);
  });
});
