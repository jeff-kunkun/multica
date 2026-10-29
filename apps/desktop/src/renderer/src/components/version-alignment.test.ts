import { describe, expect, it } from "vitest";
import { alignVersions, commitOf, sameCommit } from "./version-alignment";

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

describe("sameCommit", () => {
  it("treats a short hash as its full commit", () => {
    expect(sameCommit("690eaa5471", FULL)).toBe(true);
    expect(sameCommit(FULL, "690eaa5")).toBe(true);
    expect(sameCommit("690eaa5471", "5565a1f7f5")).toBe(false);
    expect(sameCommit("", "")).toBe(false);
  });
});

describe("alignVersions", () => {
  it("is aligned only when all three sides carry the same commit", () => {
    const got = alignVersions({
      client: "0.6.7-kun.690eaa5471",
      daemonVersion: "0.6.7-kun.690eaa5471",
      daemonCommit: FULL,
      server: FULL,
    });
    expect(got.aligned).toBe(true);
    expect(got.sides.map((s) => s.state)).toEqual(["match", "match", "match"]);
    expect(got.sides[1]?.reported).toBe(FULL);
  });

  it("marks a client without a commit in its version red", () => {
    const got = alignVersions({
      client: "0.6.7-dene2667.4",
      daemonCommit: FULL,
      server: FULL,
    });
    expect(got.aligned).toBe(false);
    expect(got.sides.map((s) => s.state)).toEqual(["no_commit", "match", "match"]);
  });

  it("marks only the odd side red when two agree", () => {
    const got = alignVersions({
      client: "0.6.7-kun.690eaa5471",
      daemonCommit: "5565a1f7f556a92427bd60f2340efd89023459e8",
      server: FULL,
    });
    expect(got.sides.map((s) => s.state)).toEqual(["match", "mismatch", "match"]);
  });

  it("marks every side red when none agree", () => {
    const got = alignVersions({
      client: "0.6.7-kun.aaaaaaa111",
      daemonCommit: "bbbbbbb222",
      server: "ccccccc333",
    });
    expect(got.sides.map((s) => s.state)).toEqual(["mismatch", "mismatch", "mismatch"]);
  });

  it("never passes a daemon that reports only dev", () => {
    const got = alignVersions({
      client: "0.6.7-kun.690eaa5471",
      daemonVersion: "dev",
      daemonCommit: "unknown",
      server: FULL,
    });
    expect(got.aligned).toBe(false);
    expect(got.sides[1]).toMatchObject({ state: "no_commit", reported: "unknown" });
  });

  it("falls back to the commit in the daemon's version", () => {
    const got = alignVersions({
      client: "0.6.7-kun.690eaa5471",
      daemonVersion: "0.6.7-kun.690eaa5471",
      server: FULL,
    });
    expect(got.aligned).toBe(true);
    expect(got.sides[1]?.reported).toBe("0.6.7-kun.690eaa5471");
  });

  it("keeps a lone commit green but the whole row red", () => {
    const got = alignVersions({ client: "0.6.7-dene2667.4", daemonCommit: FULL });
    expect(got.aligned).toBe(false);
    expect(got.sides.map((s) => s.state)).toEqual(["no_commit", "match", "no_commit"]);
  });

  it("does not pass when the server reports no version", () => {
    const got = alignVersions({
      client: "0.6.7-kun.690eaa5471",
      daemonCommit: FULL,
      server: "",
    });
    expect(got.aligned).toBe(false);
    expect(got.sides[2]).toMatchObject({ state: "no_commit", reported: "" });
  });
});
