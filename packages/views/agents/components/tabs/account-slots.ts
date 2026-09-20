// Numbered account slots, for every CLI family that owns a slot registry.
//
// A family (`@multica/core/agents/account-slot-families`, generated from the
// JSON the Go daemon embeds) says where its accounts live and which
// `runtime_config` key holds an agent's registered slot numbers:
// `runtime_config[family.runtimeConfigKey] = { accounts: [1, 2, …] }`.
// Slot 1 is the CLI's own directory and is always part of the list.
//
// What a registered slot MEANS differs by family, and only by the family's
// `rotatesOnQuota` flag:
// - agy: the list is the rotation allow-list the backend reads
//   (`server/pkg/agent/agy_quota.go`), so both sides must agree on its shape.
// - dsh / claude: the list only registers a directory for this agent so it can
//   be bound before it exists on disk. Nothing rotates; the bound directory
//   reaches the task through the family's env lever.
//
// Slots are host-only paths. This module never reads a credential; sign-in and
// quota state come from the daemon's `agent_accounts` report.

import {
  type AccountSlotFamily,
  MAX_ACCOUNT_SLOT_NUMBER,
} from "@multica/core/agents/account-slot-families";

export type NumberedSlotId = `account${number}`;

/** Registered slot numbers per CLI id. A CLI without a family has no entry. */
export type AccountSlotLists = Readonly<Record<string, readonly number[]>>;

const ACCOUNT_SLOT_RE = /^account(\d+)$/;

function isSlotNumber(n: number): boolean {
  return Number.isInteger(n) && n >= 1 && n <= MAX_ACCOUNT_SLOT_NUMBER;
}

export function parseAccountNumber(slot: string): number | null {
  const match = slot.match(ACCOUNT_SLOT_RE);
  if (!match) return null;
  const n = Number(match[1]);
  return isSlotNumber(n) ? n : null;
}

export function numberedSlotId(account: number): NumberedSlotId {
  return `account${account}`;
}

export function isNumberedAccountSlot(slot: string): slot is NumberedSlotId {
  return parseAccountNumber(slot) !== null;
}

/** First unused number from 2 up; `MAX_ACCOUNT_SLOT_NUMBER + 1` when the family is full. */
export function nextAccountNumber(accounts: readonly number[]): number {
  const used = new Set(accounts);
  for (let n = 2; n <= MAX_ACCOUNT_SLOT_NUMBER; n += 1) {
    if (!used.has(n)) return n;
  }
  return MAX_ACCOUNT_SLOT_NUMBER + 1;
}

/** Sorted, de-duplicated, in range, and always containing slot 1. */
export function normalizeAccountNumbers(accounts: readonly number[]): number[] {
  const unique = new Set<number>([1]);
  for (const n of accounts) {
    if (isSlotNumber(n)) unique.add(n);
  }
  return [...unique].sort((a, b) => a - b);
}

/** Directory name of a slot under the host home: `.dsh`, `.dsh-account2`, … */
export function slotDirectoryLeaf(family: AccountSlotFamily, account: number): string {
  return account <= 1 ? family.baseDir : `${family.accountDirPrefix}${account}`;
}

function basename(path: string): string {
  const parts = path.trim().replace(/[/\\]+$/, "").split(/[/\\]/);
  return parts[parts.length - 1] ?? "";
}

/**
 * The slot a bound directory stands for, or null when it lives outside the
 * family's numbered convention (a custom path). An empty value means nothing is
 * bound, so the CLI's own directory — slot 1 — is in effect.
 */
export function slotNumberFromDirectory(
  family: AccountSlotFamily,
  directory: string,
): number | null {
  if (!directory.trim()) return 1;
  const base = basename(directory);
  if (base === family.baseDir) return 1;
  if (!base.startsWith(family.accountDirPrefix)) return null;
  const suffix = base.slice(family.accountDirPrefix.length);
  return /^\d+$/.test(suffix) ? parseAccountNumber(`account${suffix}`) : null;
}

/**
 * Read a family's registered slots. A saved list wins as-is; an agent that
 * never saved one gets the family's defaults plus the numbered slot its
 * binding already points at, matching what the backend seeds for agy.
 */
export function parseSlotsConfig(
  family: AccountSlotFamily,
  runtimeConfig: Record<string, unknown> | null | undefined,
  boundDirectory = "",
): number[] {
  const raw = runtimeConfig?.[family.runtimeConfigKey];
  const persisted =
    raw && typeof raw === "object" && !Array.isArray(raw)
      ? (raw as { accounts?: unknown }).accounts
      : undefined;
  if (Array.isArray(persisted)) {
    return normalizeAccountNumbers(
      persisted.filter((value): value is number => typeof value === "number"),
    );
  }
  const seeded: number[] = [...family.defaultAccounts];
  const bound = slotNumberFromDirectory(family, boundDirectory);
  if (bound !== null) seeded.push(bound);
  return normalizeAccountNumbers(seeded);
}

/** Write one family's list, leaving every other `runtime_config` key untouched. */
export function writeSlotsConfig(
  family: AccountSlotFamily,
  runtimeConfig: Record<string, unknown> | null | undefined,
  accounts: readonly number[],
): Record<string, unknown> {
  return {
    ...(runtimeConfig ?? {}),
    [family.runtimeConfigKey]: { accounts: normalizeAccountNumbers(accounts) },
  };
}

/** Order-insensitive only through normalisation: both sides are normalised lists. */
export function sameSlotList(a: readonly number[], b: readonly number[]): boolean {
  return a.length === b.length && a.every((n, index) => n === b[index]);
}
