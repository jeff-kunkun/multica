import { execFile } from "node:child_process";

/**
 * How the running macOS bundle is signed, as reported by `codesign -dv`.
 *
 * - `developer-id`: signed with a Developer ID Application certificate — the
 *   only case where Squirrel.Mac will accept a downloaded update.
 * - `adhoc`: `codesign -s -` (what CI produces without Apple credentials).
 * - `unsigned`: no signature at all.
 * - `unknown`: codesign was unavailable or printed something unexpected.
 */
export type MacSigningStatus = "developer-id" | "adhoc" | "unsigned" | "unknown";

/**
 * Parse `codesign -dv --verbose=4` output. codesign writes its report to
 * stderr and exits non-zero for unsigned binaries, so callers pass whatever
 * text they got regardless of exit status.
 */
export function parseCodesignOutput(output: string): MacSigningStatus {
  if (/^Authority=Developer ID Application/m.test(output)) return "developer-id";
  if (/^Signature=adhoc$/m.test(output)) return "adhoc";
  if (/code object is not signed at all/.test(output)) return "unsigned";
  // A signed bundle without an Authority line only happens for ad-hoc
  // signatures on older codesign builds that omit the Signature= line.
  if (/^TeamIdentifier=not set$/m.test(output)) return "adhoc";
  return "unknown";
}

type CodesignRunner = (path: string) => Promise<string>;

const runCodesign: CodesignRunner = (path) =>
  new Promise((resolve) => {
    execFile(
      "codesign",
      ["-dv", "--verbose=4", path],
      { encoding: "utf-8", timeout: 10_000 },
      (_err, stdout, stderr) => {
        // codesign reports on stderr; a non-zero exit still carries the
        // "not signed at all" diagnostic we want to classify.
        resolve(`${stdout ?? ""}\n${stderr ?? ""}`);
      },
    );
  });

/**
 * Probe the signing status of the bundle that contains `executablePath`.
 * Never throws: an exec failure (no codesign on PATH, timeout) resolves to
 * `unknown` so the updater degrades to the manual path instead of crashing
 * the startup check.
 */
export async function detectMacSigning(
  executablePath: string,
  run: CodesignRunner = runCodesign,
): Promise<MacSigningStatus> {
  try {
    return parseCodesignOutput(await run(executablePath));
  } catch {
    return "unknown";
  }
}
