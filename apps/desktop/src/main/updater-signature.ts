import { execFile } from "node:child_process";

const MAC_APP_MARKER = ".app/Contents/MacOS/";

/** Bundle path Squirrel.Mac / codesign expect, derived from the executable. */
export function macAppBundlePath(exePath: string): string {
  const index = exePath.indexOf(MAC_APP_MARKER);
  if (index === -1) return exePath;
  return exePath.slice(0, index + ".app".length);
}

/**
 * Developer ID Application is the authority Squirrel.Mac can keep across
 * versions. Ad-hoc signatures (`Signature=adhoc`, `flags=0x2(adhoc)`) do not
 * carry it, so the install-time team check cannot succeed.
 */
export function hasDeveloperIdApplicationAuthority(codesignOutput: string): boolean {
  return /(?:^|\n)\s*Authority=Developer ID Application:/i.test(codesignOutput);
}

export function shouldRequireManualDownload(input: {
  platform: NodeJS.Platform;
  packaged: boolean;
  signatureText: string | null;
}): boolean {
  if (input.platform !== "darwin") return false;
  // Unpackaged dev builds and unreadable signatures cannot pass Squirrel.Mac's
  // Developer ID check. Fail closed: offer the release page instead of an
  // install that will be rejected.
  if (!input.packaged || input.signatureText == null) return true;
  return !hasDeveloperIdApplicationAuthority(input.signatureText);
}

/** `codesign -dv` writes the authority list to stderr and exits 0 when signed. */
export function readCodesignOutput(bundlePath: string): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(
      "codesign",
      ["-dv", "--verbose=4", bundlePath],
      { timeout: 8_000 },
      (error, stdout, stderr) => {
        const output = `${stdout ?? ""}\n${stderr ?? ""}`.trim();
        if (output) {
          resolve(output);
          return;
        }
        reject(error ?? new Error(`codesign returned no output for ${bundlePath}`));
      },
    );
  });
}
