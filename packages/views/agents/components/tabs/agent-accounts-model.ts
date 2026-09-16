// Domain model for the agent "accounts" tab (design C5: summary bar + drawer).
//
// Everything the accounts surface decides before it touches the DOM lives
// here: parsing the daemon-reported account list, grouping it by CLI, folding
// the agy numbered-slot pool into the rows the drawer shows, mapping per-account
// status, choosing one of the four page states, and turning "save and switch"
// into the write the caller must perform. The tab and drawer components
// (DENE-308) render this model and must not re-derive its rules.
//
// Invariants from the design doc:
// - Credential values never enter this layer. The daemon reports a `key_ref`
//   NAME plus a `signed_in` existence flag; no function here reads or composes
//   credential material, and a `key_ref` no name could have (whitespace,
//   control characters, absurd length) is dropped instead of passed through.
// - Switching only rewrites the agent's binding (`custom_args` / one env key).
//   No function here produces a filesystem action against an account directory.
// - Empty and error states never produce a write action.

import { parseWithFallback } from "@multica/core/api/schema";
import { z } from "zod";
import {
  expandHomePrefix,
  getGeminiDir,
  isAbsoluteFsPath,
  loginDirectory,
  normalizeAccountNumbers,
  numberedSlotId,
  parseAccountNumber,
  resolveHomeDir,
  setGeminiDir,
} from "./agy-account-slots";

/** Runtime metadata key holding the daemon-reported account list. */
export const AGENT_ACCOUNTS_METADATA_KEY = "agent_accounts";
/** Runtime metadata key present only when the daemon's probe failed. */
export const AGENT_ACCOUNTS_ERROR_METADATA_KEY = "agent_accounts_error";

/**
 * CLI ids the daemon reports. Server-driven: an entry carrying anything else
 * is dropped by the parser. The array order is also the fixed group order the
 * drawer renders, so grouping never depends on object key order.
 */
export const AGENT_ACCOUNT_CLIS = ["dsh", "agy", "codex", "claude", "cursor"] as const;

export type AgentAccountCli = (typeof AGENT_ACCOUNT_CLIS)[number];

/** Account id the daemon derives from a CLI's own default directory (`~/.dsh`, `~/.gemini`, …). */
export const DEFAULT_ACCOUNT_ID = "default";

/** The only `custom_args` flag this model knows how to write (see `setGeminiDir`). */
const GEMINI_DIR_FLAG = "--gemini_dir";

const ENV_LEVER_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const CUSTOM_ARGS_LEVER_RE = /^custom_args:(--[A-Za-z0-9_-]+)$/;
const MAX_KEY_REF_LENGTH = 128;
const MAX_ERROR_LENGTH = 500;
const MAX_BASE_URL_LENGTH = 512;

/** One account as reported by the daemon, validated and normalised. */
export type AgentAccount = {
  cli: AgentAccountCli;
  /** Stable id within the CLI (`default`, `account2`, …), derived from the directory name. */
  account: string;
  /** Directory that holds this account's credentials, as reported by the daemon. */
  home: string;
  base_url: string;
  /** Name of the credential reference. Never the credential itself. */
  key_ref: string;
  /** Raw binding lever; `""` means this account cannot be switched from the UI. */
  lever: string;
  signed_in: boolean;
  /** Unix seconds when an exhausted quota resets; `0` when the account is not exhausted. */
  quota_reset_at: number;
};

/** Runtime device shape this model reads: only `metadata` matters. */
export type AgentAccountsRuntime =
  | { metadata?: Record<string, unknown> | null }
  | null
  | undefined;

export type ParsedAgentAccounts = {
  accounts: AgentAccount[];
  /** Raw daemon probe error; `""` when the probe reported none. */
  error: string;
};

/**
 * Agent fields that decide which account is currently in effect and how it is
 * switched. A structural subset of `Agent`, so callers can pass the agent
 * object directly and add `custom_env` / `provider` / `runtime_home` from the
 * env endpoint and the runtime device.
 */
export type AgentAccountBinding = {
  /** `agent.custom_args` — where the agy `--gemini_dir` lever lives. */
  custom_args?: readonly string[] | null;
  /**
   * `agent.custom_env` — where env levers live. The agent payload redacts this
   * map (MUL-2600); callers pass the map read from `GET /api/agents/{id}/env`.
   * `undefined` means "not loaded", which is treated as "no override", matching
   * a fresh agent.
   */
  custom_env?: Record<string, string> | null;
  /** Host home directory (`runtimeHomeDir(runtimeDevice)`) used to expand `~` values. */
  runtime_home?: string | null;
  /** `runtimeDevice.provider` — decides which CLI the agent actually runs. */
  provider?: string | null;
};

export type AgentAccountLever =
  | { kind: "custom_args"; flag: string }
  | { kind: "env"; key: string }
  | { kind: "none" }
  | { kind: "unknown" };

export type AgentAccountGroup = {
  cli: AgentAccountCli;
  /** Raw lever shared by the group's accounts (the contract makes it per-CLI). */
  lever: string;
  /** False when this CLI cannot be switched from the UI (lever `""` or an unknown shape). */
  switchable: boolean;
  accounts: AgentAccount[];
};

export type AgentAccountStatus =
  | { kind: "signed_in" }
  | { kind: "signed_out" }
  | { kind: "quota_exhausted"; reset_at: number; reset_at_ms: number };

export type AccountsViewStateKind = "loading" | "error" | "empty" | "ready";

export type AccountsViewState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "empty" }
  | { kind: "ready" };

export type AccountSwitchUnsupportedReason =
  /** The list is loading, empty or in error — never write from an untrusted list. */
  | "view_not_ready"
  /** Nothing selected to switch to. */
  | "no_target"
  /** The daemon reported an empty lever: this account is read-only. */
  | "no_lever"
  /** Lever shape this client does not know how to write (newer daemon). */
  | "unsupported_lever"
  /** The target home is missing or not an absolute path, so it cannot be written. */
  | "invalid_home";

export type AccountSwitchPlan =
  | { kind: "custom_args"; custom_args: string[] }
  | { kind: "env"; key: string; value: string }
  | { kind: "unsupported"; reason: AccountSwitchUnsupportedReason }
  /** The target is already the account in effect — "save and switch" must not write. */
  | { kind: "noop" };

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function normalizeDirectory(value: string): string {
  const stripped = value.trim().replace(/[/\\]+$/, "");
  // Keep a bare Windows drive root ("C:\") intact instead of trimming it to "C:".
  return /^[A-Za-z]:$/.test(stripped) ? `${stripped}\\` : stripped;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/**
 * Keep `key_ref` to the name the design says it holds. Whitespace, control
 * characters and absurd lengths cannot be a name and are refused, which is as
 * far as a shape check can honestly go — a credential that happens to look
 * like a bare name is indistinguishable here, so keeping values out of this
 * field is the daemon's contract (DENE-306), not something this layer can
 * detect.
 */
function isKeyRefName(value: string): boolean {
  const chars = Array.from(value);
  if (chars.length === 0 || chars.length > MAX_KEY_REF_LENGTH) return false;
  // Written as a loop rather than a character class so the control-character
  // range stays readable and does not need a `no-control-regex` exception.
  return chars.every((char) => {
    if (/\s/.test(char)) return false;
    const code = char.codePointAt(0) ?? 0;
    return code > 0x1f && code !== 0x7f;
  });
}

function asKeyRef(value: unknown): string {
  const trimmed = asString(value);
  return isKeyRefName(trimmed) ? trimmed : "";
}

function asUnixSeconds(value: unknown): number {
  const numeric =
    typeof value === "number" ? value : typeof value === "string" ? Number(value) : Number.NaN;
  return Number.isFinite(numeric) && numeric > 0 ? Math.floor(numeric) : 0;
}

/** Keeps a daemon error readable as the single line the drawer shows. */
function asErrorText(value: unknown): string {
  if (typeof value !== "string") return "";
  return value.replace(/\s+/g, " ").trim().slice(0, MAX_ERROR_LENGTH);
}

// Identity fields are strict: an entry that is missing `cli` / `account` /
// `home` is unusable and is dropped whole. Decorative fields are total on
// purpose — a drifted `signed_in` or `quota_reset_at` must not cost us a valid
// account row.
const accountEntrySchema = z.object({
  cli: z.string(),
  account: z.string(),
  home: z.string(),
  base_url: z.unknown().transform(asString),
  key_ref: z.unknown().transform(asKeyRef),
  lever: z.unknown().transform(asString),
  signed_in: z.unknown().transform((value) => value === true),
  quota_reset_at: z.unknown().transform(asUnixSeconds),
});

const accountsArraySchema = z.array(z.unknown());
const accountsErrorSchema = z.string();

function isAgentAccountCli(value: string): value is AgentAccountCli {
  return (AGENT_ACCOUNT_CLIS as readonly string[]).includes(value);
}

function normalizeEntry(entry: z.infer<typeof accountEntrySchema>): AgentAccount | null {
  // Matched case-insensitively so a daemon that capitalises the id still lands
  // in a group instead of disappearing from the drawer.
  const cli = entry.cli.trim().toLowerCase();
  // Server-driven enum: an unknown CLI has no group, no lever and no copy, so
  // the default branch drops it instead of rendering a half-blank row.
  if (!isAgentAccountCli(cli)) return null;
  const account = entry.account.trim();
  const home = normalizeDirectory(entry.home);
  if (!account || !home) return null;
  return {
    cli,
    account,
    home,
    base_url: entry.base_url.slice(0, MAX_BASE_URL_LENGTH),
    key_ref: entry.key_ref,
    lever: entry.lever,
    signed_in: entry.signed_in,
    quota_reset_at: entry.quota_reset_at,
  };
}

/**
 * Parse `agent_accounts` / `agent_accounts_error` out of runtime metadata.
 *
 * Metadata is free-form JSON written by whichever daemon version the user
 * runs, so this never throws: a missing key (older daemon) degrades to an
 * empty list, a non-array payload is logged by `parseWithFallback` and
 * degrades to an empty list, and malformed entries are dropped one by one
 * instead of failing the whole list. Duplicate `(cli, account)` pairs keep the
 * first occurrence.
 */
export function parseAgentAccounts(runtime: AgentAccountsRuntime): ParsedAgentAccounts {
  const metadata = isRecord(runtime?.metadata) ? runtime.metadata : null;

  const accounts: AgentAccount[] = [];
  const rawList = metadata ? metadata[AGENT_ACCOUNTS_METADATA_KEY] : undefined;
  if (rawList !== undefined) {
    const list = parseWithFallback(rawList, accountsArraySchema, [], {
      endpoint: "runtime.metadata.agent_accounts",
    });
    const seen = new Set<string>();
    for (const entry of list) {
      const parsed = accountEntrySchema.safeParse(entry);
      if (!parsed.success) continue;
      const account = normalizeEntry(parsed.data);
      if (!account) continue;
      const key = `${account.cli}\u0000${account.account}`;
      if (seen.has(key)) continue;
      seen.add(key);
      accounts.push(account);
    }
  }

  const rawError = metadata ? metadata[AGENT_ACCOUNTS_ERROR_METADATA_KEY] : undefined;
  const error =
    rawError === undefined
      ? ""
      : parseWithFallback(rawError, accountsErrorSchema, "", {
          endpoint: "runtime.metadata.agent_accounts_error",
        });

  return { accounts, error: asErrorText(error) };
}

/** Parse a raw lever string into the shape this client can reason about. */
export function parseAccountLever(lever: string | null | undefined): AgentAccountLever {
  const trimmed = (lever ?? "").trim();
  if (!trimmed) return { kind: "none" };
  if (trimmed.startsWith("env:")) {
    const key = trimmed.slice("env:".length).trim();
    return ENV_LEVER_KEY_RE.test(key) ? { kind: "env", key } : { kind: "unknown" };
  }
  const customArgs = trimmed.match(CUSTOM_ARGS_LEVER_RE);
  if (customArgs?.[1]) return { kind: "custom_args", flag: customArgs[1] };
  return { kind: "unknown" };
}

/** Whether "save and switch" can produce a write for this lever at all. */
export function isSwitchableLever(lever: string | null | undefined): boolean {
  const parsed = parseAccountLever(lever);
  if (parsed.kind === "env") return true;
  return parsed.kind === "custom_args" && parsed.flag === GEMINI_DIR_FLAG;
}

/** Lever text for the drawer's group header: `DSH_HOME`, `--gemini_dir`, `""`. */
export function accountLeverLabel(lever: string | null | undefined): string {
  const parsed = parseAccountLever(lever);
  switch (parsed.kind) {
    case "env":
      return parsed.key;
    case "custom_args":
      return parsed.flag;
    case "none":
      return "";
    default:
      // Unknown shape from a newer daemon: show what it said rather than nothing.
      return (lever ?? "").trim();
  }
}

/** Deterministic, locale-independent natural order: `account2` < `account10`. */
function naturalCompare(a: string, b: string): number {
  const left = a.match(/\d+|\D+/g) ?? [];
  const right = b.match(/\d+|\D+/g) ?? [];
  const length = Math.max(left.length, right.length);
  for (let index = 0; index < length; index += 1) {
    const x = left[index];
    const y = right[index];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    const xNumeric = /^\d+$/.test(x);
    const yNumeric = /^\d+$/.test(y);
    if (xNumeric && yNumeric) {
      const diff = Number(x) - Number(y);
      if (diff !== 0) return diff < 0 ? -1 : 1;
      // Same value written differently ("02" vs "2"): longer form sorts later.
      if (x.length !== y.length) return x.length < y.length ? -1 : 1;
      continue;
    }
    if (x !== y) return x < y ? -1 : 1;
  }
  return 0;
}

/** `default` (the CLI's own directory) leads, the rest sort naturally. */
function compareAccountIds(a: string, b: string): number {
  if (a === b) return 0;
  if (a === DEFAULT_ACCOUNT_ID) return -1;
  if (b === DEFAULT_ACCOUNT_ID) return 1;
  return naturalCompare(a, b);
}

/**
 * Group accounts by CLI in the fixed contract order, each group sorted by
 * account id. Groups without accounts are omitted; unknown CLIs never reach
 * this function because the parser drops them.
 */
export function groupAccountsByCli(accounts: readonly AgentAccount[]): AgentAccountGroup[] {
  const groups: AgentAccountGroup[] = [];
  for (const cli of AGENT_ACCOUNT_CLIS) {
    const members = accounts.filter((account) => account.cli === cli);
    if (members.length === 0) continue;
    members.sort((a, b) => compareAccountIds(a.account, b.account));
    const first = members[0];
    groups.push({
      cli,
      lever: first?.lever ?? "",
      switchable: members.every((account) => isSwitchableLever(account.lever)),
      accounts: members,
    });
  }
  return groups;
}

/** Canonical flat order used whenever this model picks "the" account. */
function orderedAccounts(accounts: readonly AgentAccount[]): AgentAccount[] {
  return groupAccountsByCli(accounts).flatMap((group) => group.accounts);
}

/**
 * Slot number of a numbered account id: `default` is the CLI's own directory
 * (slot 1), `accountN` is slot N. `null` for anything else — an unknown id
 * belongs to no slot, which is what keeps a non-pool row from growing a
 * remove control.
 */
export function accountSlotNumber(
  account: Pick<AgentAccount, "account">,
): number | null {
  const id = account.account.trim();
  if (id === DEFAULT_ACCOUNT_ID) return 1;
  return parseAccountNumber(id);
}

/**
 * The drawer rows of the agy group: the agent's numbered slot pool joined with
 * the daemon's report, followed by the reported agy accounts the pool does not
 * cover.
 *
 * `runtime_config.agy_slots.accounts` is agent config, not a probe of the
 * disk — the server reads it as the pool the AGY quota failover may rotate
 * through (`ParseAgySlotAccounts`). So a pool slot whose directory the daemon
 * did not report is still a row: it is what "add account" just added, and the
 * user has to be able to see and remove it before the directory exists. A pool
 * slot the daemon DID report keeps the reported row, because its home, status
 * and credential name are the daemon's answer and this layer must not invent a
 * second one.
 *
 * Reported agy accounts outside the pool stay listed too: they exist on the
 * machine and remain switchable, so dropping them would leave the drawer
 * unable to show an account the summary bar can name.
 */
export function agyPoolAccounts(input: {
  /** Pending pool (`runtime_config.agy_slots.accounts`). */
  numbers: readonly number[];
  /** The daemon's report for this runtime, every CLI. */
  reported: readonly AgentAccount[];
  /** Host home directory, used to resolve a slot the daemon did not report. */
  homeDir: string | null;
}): AgentAccount[] {
  const reportedAgy = input.reported.filter((account) => account.cli === "agy");
  const claimed = new Set<AgentAccount>();
  const rows: AgentAccount[] = [];

  for (const number of normalizeAccountNumbers(input.numbers)) {
    const slot = numberedSlotId(number);
    const directory = loginDirectory(slot, "", input.homeDir);
    const match = reportedAgy.find((account) =>
      sameDirectory(account.home, directory),
    );
    if (match) {
      // `Set` is identity-keyed, and the parser hands out one object per row.
      claimed.add(match);
      rows.push(match);
      continue;
    }
    rows.push({
      cli: "agy",
      account: number === 1 ? DEFAULT_ACCOUNT_ID : slot,
      // Left unresolved without a host home, so the plan layer refuses to
      // write a path instead of this layer guessing one.
      home: normalizeDirectory(directory),
      base_url: "",
      key_ref: "",
      lever: `custom_args:${GEMINI_DIR_FLAG}`,
      signed_in: false,
      quota_reset_at: 0,
    });
  }

  for (const account of reportedAgy) {
    if (!claimed.has(account)) rows.push(account);
  }
  return rows;
}

const PROVIDER_CLI: Record<string, AgentAccountCli> = {
  agy: "agy",
  antigravity: "agy",
  dsh: "dsh",
  claude: "claude",
  codex: "codex",
  cursor: "cursor",
};

/** Map a runtime provider onto the CLI whose accounts it consumes. */
export function cliForProvider(provider: string | null | undefined): AgentAccountCli | null {
  const key = (provider ?? "").trim().toLowerCase();
  return PROVIDER_CLI[key] ?? null;
}

/**
 * Resolve a lever value (`~/.dsh-account2`, `/Users/you/.gemini`) to an
 * absolute directory. Relative values return null: they can never equal the
 * absolute `home` the daemon reported, so claiming a match would be a guess.
 */
function resolveBoundDirectory(
  value: string,
  agent: AgentAccountBinding | null | undefined,
): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (isAbsoluteFsPath(trimmed)) return normalizeDirectory(trimmed);
  if (trimmed === "~" || trimmed.startsWith("~/") || trimmed.startsWith("~\\")) {
    const hostHome = resolveHomeDir(trimmed, agent?.runtime_home ?? null);
    return hostHome ? normalizeDirectory(expandHomePrefix(trimmed, hostHome)) : null;
  }
  return null;
}

/** Case-insensitive only for Windows drive paths, where the OS is. */
function sameDirectory(a: string | null, b: string | null): boolean {
  if (!a || !b) return false;
  const left = normalizeDirectory(a);
  const right = normalizeDirectory(b);
  if (left === right) return true;
  const windows = /^[A-Za-z]:[/\\]/;
  return windows.test(left) && windows.test(right) && left.toLowerCase() === right.toLowerCase();
}

function envBinding(
  agent: AgentAccountBinding | null | undefined,
  key: string,
): string {
  const raw = agent?.custom_env?.[key];
  return typeof raw === "string" ? raw.trim() : "";
}

/** True when the agent's own config already points this account's lever at it. */
function accountIsExplicitlyBound(
  agent: AgentAccountBinding | null | undefined,
  account: AgentAccount,
): boolean {
  const lever = parseAccountLever(account.lever);
  if (lever.kind === "env") {
    const value = envBinding(agent, lever.key);
    if (!value) return false;
    return sameDirectory(resolveBoundDirectory(value, agent), account.home);
  }
  if (lever.kind === "custom_args" && lever.flag === GEMINI_DIR_FLAG) {
    const value = getGeminiDir([...(agent?.custom_args ?? [])]);
    if (!value.trim()) return false;
    return sameDirectory(resolveBoundDirectory(value, agent), account.home);
  }
  return false;
}

/** True when nothing binds this CLI, so its own default directory is in effect. */
function accountFallsBackToCliDefault(
  agent: AgentAccountBinding | null | undefined,
  account: AgentAccount,
): boolean {
  if (account.account !== DEFAULT_ACCOUNT_ID) return false;
  const lever = parseAccountLever(account.lever);
  switch (lever.kind) {
    case "none":
      // No binding lever exists for this CLI at all (codex / cursor).
      return true;
    case "custom_args":
      return (
        lever.flag === GEMINI_DIR_FLAG &&
        getGeminiDir([...(agent?.custom_args ?? [])]).trim() === ""
      );
    case "env":
      // `undefined` custom_env means "not loaded": assume no override, exactly
      // like an agent that never set the key.
      return envBinding(agent, lever.key) === "";
    default:
      // Unknown lever shape: whether it is bound cannot be told, so claim nothing.
      return false;
  }
}

/**
 * The account currently in effect for this agent.
 *
 * Resolution order:
 * 1. Explicit binding — a lever value that resolves to the account's `home`.
 * 2. The CLI's own default account, but only while that CLI's lever is unbound.
 *
 * When the runtime provider maps to a known CLI, only that CLI's accounts are
 * considered; the daemon reports every account on the machine, and an agy
 * agent must not be described by a dsh account. Returns null when nothing
 * matches — an unknown account is better than a wrong one.
 */
export function resolveCurrentAccount(
  agent: AgentAccountBinding | null | undefined,
  accounts: readonly AgentAccount[],
): AgentAccount | null {
  const ordered = orderedAccounts(accounts);
  const providerCli = cliForProvider(agent?.provider);
  const scoped =
    providerCli && ordered.some((account) => account.cli === providerCli)
      ? ordered.filter((account) => account.cli === providerCli)
      : ordered;

  const bound = scoped.find((account) => accountIsExplicitlyBound(agent, account));
  if (bound) return bound;
  return scoped.find((account) => accountFallsBackToCliDefault(agent, account)) ?? null;
}

/**
 * Map an account onto the status the row shows.
 *
 * `signed_out` wins over an exhausted quota: without credentials there is
 * nothing to wait for. A reset time that has already passed means the quota is
 * usable again, which reads as a plain signed-in account.
 */
export function accountStatus(
  account: AgentAccount,
  nowMs: number = Date.now(),
): AgentAccountStatus {
  if (account.signed_in !== true) return { kind: "signed_out" };
  const resetAt = account.quota_reset_at;
  if (Number.isFinite(resetAt) && resetAt > 0 && resetAt * 1000 > nowMs) {
    return { kind: "quota_exhausted", reset_at: resetAt, reset_at_ms: resetAt * 1000 };
  }
  return { kind: "signed_in" };
}

/**
 * Pick one of the four page states, in precedence order: an in-flight read
 * wins over a stale error, a probe error wins over an empty list (the design
 * renders them differently and only this field can tell them apart), and only
 * a non-empty list with no error is `ready`.
 */
export function accountsViewState(input: {
  accounts: readonly AgentAccount[];
  error?: string | null;
  loading?: boolean;
}): AccountsViewState {
  if (input.loading === true) return { kind: "loading" };
  const message = typeof input.error === "string" ? input.error.trim() : "";
  if (message !== "") return { kind: "error", message };
  if (input.accounts.length === 0) return { kind: "empty" };
  return { kind: "ready" };
}

/**
 * The single gate for the manage drawer. Loading, empty and error all stay
 * read-only by design: an untrustworthy list gets no editing surface.
 */
export function canManageAccounts(state: AccountsViewState): boolean {
  return state.kind === "ready";
}

/**
 * Turn "save and switch" into the write the caller performs.
 *
 * The plan only ever describes an agent-field write — agy's `--gemini_dir`
 * inside `custom_args`, or one env key — never a filesystem action. Callers
 * must apply the `env` plan through the env endpoint (a `custom_env` field on
 * the agent update is rejected with 400) and must touch only the returned key.
 *
 * Precedence: an untrustworthy view and a missing lever both outrank "already
 * current" — when there is nothing to write, the reason to write nothing is
 * the more useful answer.
 */
export function planAccountSwitch(
  agent: AgentAccountBinding | null | undefined,
  target: AgentAccount | null | undefined,
  view?: AccountsViewState,
): AccountSwitchPlan {
  if (view && view.kind !== "ready") return { kind: "unsupported", reason: "view_not_ready" };
  if (!target) return { kind: "unsupported", reason: "no_target" };

  const lever = parseAccountLever(target.lever);
  if (lever.kind === "none") return { kind: "unsupported", reason: "no_lever" };
  if (lever.kind === "unknown") return { kind: "unsupported", reason: "unsupported_lever" };

  if (accountIsExplicitlyBound(agent, target) || accountFallsBackToCliDefault(agent, target)) {
    return { kind: "noop" };
  }

  const home = normalizeDirectory(target.home);
  if (!isAbsoluteFsPath(home)) return { kind: "unsupported", reason: "invalid_home" };

  if (lever.kind === "env") return { kind: "env", key: lever.key, value: home };
  if (lever.flag === GEMINI_DIR_FLAG) {
    // The default account is written explicitly too: the flag value is that
    // account's own directory, which is what the CLI would have used anyway.
    return {
      kind: "custom_args",
      custom_args: setGeminiDir([...(agent?.custom_args ?? [])], home),
    };
  }
  return { kind: "unsupported", reason: "unsupported_lever" };
}
