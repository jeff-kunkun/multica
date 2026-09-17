// @vitest-environment node
import { delimiter, join } from "path";
import { describe, expect, it } from "vitest";

import { applyFallbackPathDirs, fallbackPathDirs } from "./path-fallback";

describe("fallbackPathDirs", () => {
  it("lists the user-local install dir ahead of the machine-wide ones", () => {
    // #DENE-507: a Homebrew/npm global copy in /opt/homebrew/bin used to
    // shadow the newer standalone install in ~/.local/bin, because the daemon
    // resolves agent CLIs with the first PATH match.
    const dirs = fallbackPathDirs("/Users/kunkun");
    expect(dirs.indexOf(join("/Users/kunkun", ".local/bin"))).toBeLessThan(
      dirs.indexOf("/opt/homebrew/bin"),
    );
    expect(dirs.indexOf(join("/Users/kunkun", ".local/bin"))).toBeLessThan(
      dirs.indexOf("/usr/local/bin"),
    );
  });

  it("keeps Homebrew ahead of the Intel/manual /usr/local and builds the home entry", () => {
    expect(fallbackPathDirs("/home/u")).toEqual([
      join("/home/u", ".local/bin"),
      "/opt/homebrew/bin",
      "/usr/local/bin",
    ]);
  });
});

describe("applyFallbackPathDirs", () => {
  it("prepends the fallbacks so the inherited PATH survives behind them", () => {
    expect(applyFallbackPathDirs("/usr/bin:/bin", "/home/u")).toBe(
      [
        join("/home/u", ".local/bin"),
        "/opt/homebrew/bin",
        "/usr/local/bin",
        "/usr/bin",
        "/bin",
      ].join(delimiter),
    );
  });

  it("returns just the fallbacks for an unset or empty PATH, with no empty segment", () => {
    const expected = [
      join("/home/u", ".local/bin"),
      "/opt/homebrew/bin",
      "/usr/local/bin",
    ].join(delimiter);
    expect(applyFallbackPathDirs(undefined, "/home/u")).toBe(expected);
    expect(applyFallbackPathDirs("", "/home/u")).toBe(expected);
  });
});
