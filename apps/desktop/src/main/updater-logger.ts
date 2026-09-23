import { appendFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";

type LogFn = (message?: unknown, ...rest: unknown[]) => void;

/** electron-updater's logger shape. Assigned to `autoUpdater.logger`. */
export interface UpdaterLogger {
  info: LogFn;
  warn: LogFn;
  error: LogFn;
  debug: LogFn;
}

export function updaterLogFilePath(logsDirectory: string): string {
  return join(logsDirectory, "updater.log");
}

function formatLogArg(value: unknown): string {
  if (typeof value === "string") return value;
  if (value instanceof Error) return value.stack || value.message;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

/**
 * Append-only file logger for the update pipeline. Packaged apps write
 * `app.getPath("logs")/updater.log` so a failed download can be diagnosed
 * after the window is gone.
 */
export function createUpdaterLogger(logsDirectory: string): UpdaterLogger {
  const filePath = updaterLogFilePath(logsDirectory);
  const write = (level: string, args: unknown[]): void => {
    const line = `${new Date().toISOString()} [${level}] ${args.map(formatLogArg).join(" ")}\n`;
    try {
      mkdirSync(dirname(filePath), { recursive: true });
      appendFileSync(filePath, line);
    } catch (err) {
      console.error("Failed to write updater log:", err);
    }
  };
  return {
    info: (...args) => write("info", args),
    warn: (...args) => write("warn", args),
    error: (...args) => write("error", args),
    debug: (...args) => write("debug", args),
  };
}
