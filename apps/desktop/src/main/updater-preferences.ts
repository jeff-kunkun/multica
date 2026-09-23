import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { ReleaseChannel, UpdaterPreferences } from "../shared/updater-types";

export const DEFAULT_UPDATER_PREFERENCES: UpdaterPreferences = {
  automaticUpdates: true,
  releaseChannel: "stable",
};

export function updaterPreferencesPath(userDataPath: string): string {
  return join(userDataPath, "updater-preferences.json");
}

function releaseChannelFrom(value: unknown): ReleaseChannel {
  return value === "test" ? "test" : "stable";
}

function parseUpdaterPreferences(value: unknown): UpdaterPreferences {
  if (typeof value !== "object" || value === null) {
    return { ...DEFAULT_UPDATER_PREFERENCES };
  }
  const candidate = value as {
    automaticUpdates?: unknown;
    releaseChannel?: unknown;
  };
  if (typeof candidate.automaticUpdates !== "boolean") {
    return { ...DEFAULT_UPDATER_PREFERENCES };
  }
  return {
    automaticUpdates: candidate.automaticUpdates,
    releaseChannel: releaseChannelFrom(candidate.releaseChannel),
  };
}

export async function loadUpdaterPreferences(
  filePath: string,
): Promise<UpdaterPreferences> {
  try {
    return parseUpdaterPreferences(JSON.parse(await readFile(filePath, "utf-8")));
  } catch {
    return { ...DEFAULT_UPDATER_PREFERENCES };
  }
}

export async function saveUpdaterPreferences(
  filePath: string,
  preferences: UpdaterPreferences,
): Promise<void> {
  await mkdir(dirname(filePath), { recursive: true });
  const temporaryPath = `${filePath}.tmp`;
  await writeFile(temporaryPath, JSON.stringify(preferences, null, 2), "utf-8");
  await rename(temporaryPath, filePath);
}
