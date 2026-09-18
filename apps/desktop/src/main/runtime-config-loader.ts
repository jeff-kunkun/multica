import { app } from "electron";
import { mkdir, readFile, rename, unlink, writeFile } from "fs/promises";
import { dirname, join } from "path";
import {
  DEFAULT_ENTRY_RUNTIME_CONFIG,
  DEFAULT_RUNTIME_CONFIG,
  parseRuntimeConfig,
  runtimeConfigFromDevEnv,
  runtimeConfigFromServerUrl,
  type RuntimeConfig,
  type RuntimeConfigEnv,
  type RuntimeConfigResult,
  type RuntimeConfigSwitchResult,
} from "../shared/runtime-config";

export type RuntimeConfigSwitchTarget =
  | { type: "official" }
  | { type: "url"; url: string };

export async function loadRuntimeConfig(options: {
  isDev: boolean;
  env: RuntimeConfigEnv;
  configPath?: string;
}): Promise<RuntimeConfigResult> {
  if (options.isDev) {
    try {
      return { ok: true, config: runtimeConfigFromDevEnv(options.env) };
    } catch (err) {
      return { ok: false, error: { message: errorMessage(err) } };
    }
  }

  const configPath = options.configPath ?? desktopConfigPath();
  try {
    const raw = await readFile(configPath, "utf-8");
    return { ok: true, config: parseRuntimeConfig(raw) };
  } catch (err) {
    if (isMissingFileError(err)) {
      return { ok: true, config: { ...DEFAULT_ENTRY_RUNTIME_CONFIG } };
    }
    return {
      ok: false,
      error: {
        message: `Invalid ${configPath}: ${errorMessage(err)}`,
      },
    };
  }
}

export function desktopConfigPath(): string {
  return join(app.getPath("home"), ".multica", "desktop.json");
}

export async function switchRuntimeConfig(options: {
  configPath: string;
  target: RuntimeConfigSwitchTarget;
}): Promise<RuntimeConfigSwitchResult> {
  try {
    const config =
      options.target.type === "official"
        ? { ...DEFAULT_RUNTIME_CONFIG }
        : runtimeConfigFromServerUrl(options.target.url);

    // Both targets are written out. Deleting the file used to mean "official
    // cloud", but the absent-file fallback is now the fork's self-hosted
    // entry default, so official cloud has to be stated explicitly.
    await writeRuntimeConfigFile(options.configPath, config);
    return { ok: true, config };
  } catch (err) {
    return { ok: false, error: errorMessage(err) };
  }
}

async function writeRuntimeConfigFile(
  configPath: string,
  config: RuntimeConfig,
): Promise<void> {
  await mkdir(dirname(configPath), { recursive: true });
  const temporaryPath = `${configPath}.tmp`;
  const payload: RuntimeConfig = {
    schemaVersion: 1,
    apiUrl: config.apiUrl,
    wsUrl: config.wsUrl,
    appUrl: config.appUrl,
  };
  try {
    await writeFile(
      temporaryPath,
      `${JSON.stringify(payload, null, 2)}\n`,
      "utf-8",
    );
    await rename(temporaryPath, configPath);
  } catch (err) {
    await unlink(temporaryPath).catch(() => undefined);
    throw err;
  }
}

function isMissingFileError(err: unknown): boolean {
  return Boolean(
    err &&
      typeof err === "object" &&
      "code" in err &&
      (err as NodeJS.ErrnoException).code === "ENOENT",
  );
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export type { RuntimeConfig, RuntimeConfigResult, RuntimeConfigSwitchResult };
