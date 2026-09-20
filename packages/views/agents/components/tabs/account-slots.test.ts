// @vitest-environment node
//
// Canonical suite for the numbered account-slot registry (DENE-678): the
// parse/write pair for `runtime_config[family.runtimeConfigKey]` and the slot
// ↔ directory mapping, for every CLI family in the generated table.
//
// The agy rows double as the compatibility contract: `agy_slots` is the key the
// backend rotation reads and the key existing workspaces already store, so its
// name, defaults and shape are pinned here exactly as they were before the
// registry became per-CLI. The Go side pins the same facts against
// `agy_quota.go` in `server/pkg/agent/account_slot_families_test.go`.

import { describe, expect, it } from "vitest";
import {
  ACCOUNT_SLOT_FAMILIES,
  MAX_ACCOUNT_SLOT_NUMBER,
  accountSlotFamily,
} from "@multica/core/agents/account-slot-families";
import {
  isNumberedAccountSlot,
  nextAccountNumber,
  normalizeAccountNumbers,
  numberedSlotId,
  parseAccountNumber,
  parseSlotsConfig,
  sameSlotList,
  slotDirectoryLeaf,
  slotNumberFromDirectory,
  writeSlotsConfig,
} from "./account-slots";

const AGY = accountSlotFamily("agy")!;
const DSH = accountSlotFamily("dsh")!;
const CLAUDE = accountSlotFamily("claude")!;

describe("account slot family table", () => {
  it("gives agy, dsh and claude a registry and leaves codex / cursor without one", () => {
    expect(ACCOUNT_SLOT_FAMILIES.map((family) => family.cli)).toEqual([
      "agy",
      "dsh",
      "claude",
    ]);
    // CODEX_HOME / CURSOR_DATA_DIR are rewritten per task by the daemon, so a
    // slot registered for them could never take effect.
    expect(accountSlotFamily("codex")).toBeNull();
    expect(accountSlotFamily("cursor")).toBeNull();
  });

  it("keeps agy's stored key and marks it as the only rotating family", () => {
    expect(AGY.runtimeConfigKey).toBe("agy_slots");
    expect(DSH.runtimeConfigKey).toBe("dsh_slots");
    expect(CLAUDE.runtimeConfigKey).toBe("claude_slots");
    expect(
      ACCOUNT_SLOT_FAMILIES.filter((family) => family.rotatesOnQuota).map(
        (family) => family.cli,
      ),
    ).toEqual(["agy"]);
  });

  it("binds each family through the lever the daemon reports", () => {
    expect(AGY.lever).toBe("custom_args:--gemini_dir");
    expect(DSH.lever).toBe("env:DSH_HOME");
    expect(CLAUDE.lever).toBe("env:CLAUDE_CONFIG_DIR");
  });
});

describe("slot numbers", () => {
  it("round-trips a slot id and refuses anything outside the numbered range", () => {
    expect(numberedSlotId(4)).toBe("account4");
    expect(parseAccountNumber("account4")).toBe(4);
    expect(parseAccountNumber("custom")).toBeNull();
    expect(parseAccountNumber("account0")).toBeNull();
    expect(parseAccountNumber(`account${MAX_ACCOUNT_SLOT_NUMBER + 1}`)).toBeNull();
    expect(isNumberedAccountSlot("account2")).toBe(true);
    expect(isNumberedAccountSlot("default")).toBe(false);
  });

  it("adds the next unused number so plus fills the first gap", () => {
    expect(nextAccountNumber([1, 2, 3])).toBe(4);
    expect(nextAccountNumber([1, 2, 4])).toBe(3);
    expect(nextAccountNumber([1])).toBe(2);
    const full = Array.from({ length: MAX_ACCOUNT_SLOT_NUMBER }, (_, i) => i + 1);
    expect(nextAccountNumber(full)).toBe(MAX_ACCOUNT_SLOT_NUMBER + 1);
  });

  it("normalises to a sorted in-range list that always holds slot 1", () => {
    expect(normalizeAccountNumbers([4, 1, 4, 0, 99])).toEqual([1, 4]);
    expect(normalizeAccountNumbers([])).toEqual([1]);
    expect(normalizeAccountNumbers([2.5, 3])).toEqual([1, 3]);
    expect(sameSlotList([1, 2], [1, 2])).toBe(true);
    expect(sameSlotList([1, 2], [1, 3])).toBe(false);
    expect(sameSlotList([1], [1, 2])).toBe(false);
  });
});

describe("slot directories", () => {
  it.each([
    [AGY, ".gemini", ".gemini-account2", ".gemini-account4"],
    [DSH, ".dsh", ".dsh-account2", ".dsh-account4"],
    [CLAUDE, ".claude", ".claude-account2", ".claude-account4"],
  ])("maps %o slots onto the directories the daemon globs", (family, one, two, four) => {
    expect(slotDirectoryLeaf(family, 1)).toBe(one);
    expect(slotDirectoryLeaf(family, 2)).toBe(two);
    expect(slotDirectoryLeaf(family, 4)).toBe(four);
  });

  it("reads the slot back from a bound directory, in any spelling", () => {
    expect(slotNumberFromDirectory(AGY, "")).toBe(1);
    expect(slotNumberFromDirectory(AGY, "/Users/you/.gemini")).toBe(1);
    expect(slotNumberFromDirectory(AGY, "~/.gemini")).toBe(1);
    expect(slotNumberFromDirectory(AGY, "/Users/you/.gemini-account2")).toBe(2);
    expect(slotNumberFromDirectory(AGY, "~/.gemini-account4/")).toBe(4);
    expect(slotNumberFromDirectory(DSH, "C:\\Users\\you\\.dsh-account3")).toBe(3);
    expect(slotNumberFromDirectory(CLAUDE, "/Users/you/.claude-account2")).toBe(2);
  });

  it("calls a directory outside the numbered convention custom, not slot 1", () => {
    expect(slotNumberFromDirectory(AGY, "/Users/you/.gemini-work")).toBeNull();
    expect(slotNumberFromDirectory(DSH, "/Users/you/.dsh-accountX")).toBeNull();
    expect(slotNumberFromDirectory(DSH, "/Users/you/.dsh-account")).toBeNull();
    expect(slotNumberFromDirectory(DSH, "/Users/you/.dsh-account99")).toBeNull();
    // Another family's directory is not this family's slot.
    expect(slotNumberFromDirectory(DSH, "/Users/you/.claude-account2")).toBeNull();
  });
});

describe("runtime_config slot lists", () => {
  it("reads agy exactly as before the registry became per-CLI", () => {
    expect(parseSlotsConfig(AGY, {})).toEqual([1, 2, 3]);
    expect(parseSlotsConfig(AGY, null)).toEqual([1, 2, 3]);
    expect(parseSlotsConfig(AGY, { agy_slots: { accounts: [1, 4, 5] } })).toEqual([
      1, 4, 5,
    ]);
    expect(parseSlotsConfig(AGY, {}, "/Users/you/.gemini-account4")).toEqual([
      1, 2, 3, 4,
    ]);
    // A saved list wins over the binding: folding the bound slot back in is
    // the tab's repair step, not the parser's.
    expect(
      parseSlotsConfig(AGY, { agy_slots: { accounts: [1] } }, "/Users/you/.gemini-account4"),
    ).toEqual([1]);
  });

  it("seeds a manual family with its own directory plus the bound slot", () => {
    expect(parseSlotsConfig(DSH, {})).toEqual([1]);
    expect(parseSlotsConfig(DSH, {}, "/Users/you/.dsh-account3")).toEqual([1, 3]);
    expect(parseSlotsConfig(CLAUDE, {}, "/Users/you/.claude-work")).toEqual([1]);
    expect(parseSlotsConfig(CLAUDE, { claude_slots: { accounts: [2, 1] } })).toEqual([
      1, 2,
    ]);
  });

  it("reads each family from its own key only", () => {
    const config = {
      agy_slots: { accounts: [1, 5] },
      dsh_slots: { accounts: [1, 2] },
    };
    expect(parseSlotsConfig(AGY, config)).toEqual([1, 5]);
    expect(parseSlotsConfig(DSH, config)).toEqual([1, 2]);
    expect(parseSlotsConfig(CLAUDE, config)).toEqual([1]);
  });

  it.each([
    ["an array", []],
    ["a string", "1,2"],
    ["null", null],
    ["an object without accounts", { slots: [1, 2] }],
    ["non-array accounts", { accounts: "1,2" }],
  ])("falls back to the defaults when the stored value is %s", (_label, stored) => {
    expect(parseSlotsConfig(DSH, { dsh_slots: stored })).toEqual([1]);
    expect(parseSlotsConfig(AGY, { agy_slots: stored })).toEqual([1, 2, 3]);
  });

  it("drops non-numeric entries from a stored list instead of rejecting it", () => {
    expect(
      parseSlotsConfig(DSH, { dsh_slots: { accounts: [2, "3", null, 40, 4] } }),
    ).toEqual([1, 2, 4]);
  });

  it("writes one family's key and leaves every other key untouched", () => {
    expect(writeSlotsConfig(AGY, { mode: "local" }, [1, 4])).toEqual({
      mode: "local",
      agy_slots: { accounts: [1, 4] },
    });
    expect(
      writeSlotsConfig(DSH, { agy_slots: { accounts: [1, 2, 3] } }, [1, 2]),
    ).toEqual({
      agy_slots: { accounts: [1, 2, 3] },
      dsh_slots: { accounts: [1, 2] },
    });
    expect(writeSlotsConfig(CLAUDE, null, [2])).toEqual({
      claude_slots: { accounts: [1, 2] },
    });
  });

  it("stores a normalised list so the backend's normalisation cannot disagree", () => {
    expect(writeSlotsConfig(AGY, {}, [4, 4, 0, 40])).toEqual({
      agy_slots: { accounts: [1, 4] },
    });
  });
});
