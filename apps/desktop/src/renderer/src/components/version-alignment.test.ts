import { describe, expect, it } from "vitest";
import { commitOf, sideVersions } from "./version-alignment";

const FULL = "690eaa5471139ba8e5f60beca65e71865221272d";

describe("commitOf", () => {
  it.each([
    ["0.6.7-kun.690eaa5471", "690eaa5471"],
    [FULL, FULL],
    ["v0.6.7-kun.690EAA5471", "690eaa5471"],
    ["0.6.7-dene2667.4", ""],
    ["dev", ""],
    ["unknown", ""],
    ["0.6.7-20261029", ""],
    ["", ""],
    [undefined, ""],
  ])("reads %s as %j", (text, want) => {
    expect(commitOf(text)).toBe(want);
  });
});

describe("sideVersions", () => {
  it("lists client, daemon and server in that order with their commits", () => {
    const got = sideVersions({
      client: "0.6.7-kun.690eaa5471",
      daemonVersion: "0.6.7-kun.690eaa5471",
      daemonCommit: FULL,
      server: FULL,
    });
    expect(got).toEqual([
      { side: "client", reported: "0.6.7-kun.690eaa5471", commit: "690eaa5471" },
      { side: "daemon", reported: FULL, commit: FULL },
      { side: "server", reported: FULL, commit: FULL },
    ]);
  });

  it("keeps a version without a commit as reported", () => {
    const got = sideVersions({ client: "0.6.7-office.7", daemonVersion: "dev", server: "" });
    expect(got.map((s) => [s.reported, s.commit])).toEqual([
      ["0.6.7-office.7", ""],
      ["dev", ""],
      ["", ""],
    ]);
  });

  it("falls back to the commit in the daemon's version", () => {
    const got = sideVersions({ daemonVersion: "0.6.7-kun.690eaa5471", daemonCommit: "unknown" });
    expect(got[1]).toEqual({ side: "daemon", reported: "unknown", commit: "690eaa5471" });
  });
});
