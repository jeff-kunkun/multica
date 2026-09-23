// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  hasDeveloperIdApplicationAuthority,
  macAppBundlePath,
  shouldRequireManualDownload,
} from "./updater-signature";

const DEVELOPER_ID = `
Executable=/Applications/Multica.app/Contents/MacOS/Multica
Identifier=ai.multica.desktop
CodeDirectory v=20500 flags=0x10000(runtime)
Authority=Developer ID Application: Multica (ABCDE12345)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
`;

const AD_HOC = `
Executable=/Applications/Multica.app/Contents/MacOS/Multica
Identifier=ai.multica.desktop
CodeDirectory v=20400 flags=0x2(adhoc)
Signature=adhoc
`;

describe("macOS update signature", () => {
  it("extracts the .app bundle from the executable path", () => {
    expect(
      macAppBundlePath("/Applications/Multica.app/Contents/MacOS/Multica"),
    ).toBe("/Applications/Multica.app");
    expect(macAppBundlePath("/usr/local/bin/multica")).toBe("/usr/local/bin/multica");
  });

  it("recognizes a Developer ID Application authority and rejects ad-hoc output", () => {
    expect(hasDeveloperIdApplicationAuthority(DEVELOPER_ID)).toBe(true);
    expect(hasDeveloperIdApplicationAuthority(AD_HOC)).toBe(false);
    expect(hasDeveloperIdApplicationAuthority("")).toBe(false);
    expect(
      hasDeveloperIdApplicationAuthority("note: not an Authority=Developer ID Application line"),
    ).toBe(false);
  });

  it("requires a manual download only for darwin builds that are not Developer ID signed", () => {
    expect(
      shouldRequireManualDownload({
        platform: "darwin",
        packaged: true,
        signatureText: DEVELOPER_ID,
      }),
    ).toBe(false);
    expect(
      shouldRequireManualDownload({
        platform: "darwin",
        packaged: true,
        signatureText: AD_HOC,
      }),
    ).toBe(true);
    expect(
      shouldRequireManualDownload({
        platform: "darwin",
        packaged: true,
        signatureText: null,
      }),
    ).toBe(true);
    expect(
      shouldRequireManualDownload({
        platform: "darwin",
        packaged: false,
        signatureText: DEVELOPER_ID,
      }),
    ).toBe(true);
    for (const platform of ["win32", "linux"] as const) {
      expect(
        shouldRequireManualDownload({
          platform,
          packaged: true,
          signatureText: null,
        }),
      ).toBe(false);
    }
  });
});
