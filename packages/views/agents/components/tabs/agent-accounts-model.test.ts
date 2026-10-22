// @vitest-environment node
//
// Canonical test layer for the accounts tab's rules: parsing, grouping, status
// mapping, the four page states and the switch plan (DENE-307). These are pure
// functions with no DOM, so this suite runs under node and the DENE-308
// component suite must NOT re-run this matrix through a mount — it points here.

import { describe, expect, it } from "vitest";
import { accountSlotFamily } from "@multica/core/agents/account-slot-families";
import {
  AGENT_ACCOUNT_CLIS,
  type AccountsViewState,
  type AgentAccount,
  accountLeverLabel,
  accountStatus,
  accountsViewState,
  accountSlotNumberOf,
  canManageAccounts,
  cliForProvider,
  groupAccountsByCli,
  isSwitchableLever,
  parseAccountLever,
  parseAgentAccounts,
  planAccountSwitch,
  resolveCurrentAccount,
  boundDirectoryFor,
  nextSlotForGroup,
  removableSlots,
  slotAccountId,
  withAccountSlots,
} from "./agent-accounts-model";

const RUNTIME_HOME = "/Users/you";
const READY: AccountsViewState = { kind: "ready" };

function account(
  overrides: Partial<AgentAccount> & Pick<AgentAccount, "cli" | "account" | "home">,
): AgentAccount {
  return {
    base_url: "",
    key_ref: "",
    lever: "",
    signed_in: true,
    ...overrides,
  };
}

function runtimeWith(entries: unknown, error?: unknown): { metadata: Record<string, unknown> } {
  return {
    metadata: {
      agent_accounts: entries,
      ...(error === undefined ? {} : { agent_accounts_error: error }),
    },
  };
}

// agy is reported read-only: its own `~/.gemini`, no lever.
const AGY_DEFAULT = account({
  cli: "agy",
  account: "default",
  home: `${RUNTIME_HOME}/.gemini`,
  lever: "",
});
// What a 0.6.x daemon still reports: a numbered directory with the retired
// `--gemini_dir` lever, which this client no longer writes.
const AGY_LEGACY_ACCOUNT2 = account({
  cli: "agy",
  account: "account2",
  home: `${RUNTIME_HOME}/.gemini-account2`,
  lever: "custom_args:--gemini_dir",
});
const DSH_DEFAULT = account({
  cli: "dsh",
  account: "default",
  home: `${RUNTIME_HOME}/.dsh`,
  lever: "env:DSH_HOME",
});
const DSH_ACCOUNT2 = account({
  cli: "dsh",
  account: "account2",
  home: `${RUNTIME_HOME}/.dsh-account2`,
  lever: "env:DSH_HOME",
});
const CODEX_DEFAULT = account({
  cli: "codex",
  account: "default",
  home: `${RUNTIME_HOME}/.codex`,
  lever: "",
});
const CLAUDE_DEFAULT = account({
  cli: "claude",
  account: "default",
  home: `${RUNTIME_HOME}/.claude`,
  lever: "env:CLAUDE_CONFIG_DIR",
});
const CLAUDE_ACCOUNT2 = account({
  cli: "claude",
  account: "account2",
  home: `${RUNTIME_HOME}/.claude-account2`,
  lever: "env:CLAUDE_CONFIG_DIR",
});
const CURSOR_DEFAULT = account({
  cli: "cursor",
  account: "default",
  home: `${RUNTIME_HOME}/.cursor`,
  lever: "",
});

describe("parseAgentAccounts", () => {
  it("maps every reported field", () => {
    const runtime = runtimeWith([
      {
        cli: "dsh",
        account: "default",
        home: "/Users/you/.dsh",
        base_url: "https://api.example.test",
        key_ref: "dsh-default",
        lever: "env:DSH_HOME",
        signed_in: true,
      },
    ]);

    expect(parseAgentAccounts(runtime)).toEqual({
      accounts: [
        {
          cli: "dsh",
          account: "default",
          home: "/Users/you/.dsh",
          base_url: "https://api.example.test",
          key_ref: "dsh-default",
          lever: "env:DSH_HOME",
          signed_in: true,
        },
      ],
      error: "",
    });
  });

  it("degrades to an empty list when an older daemon never reported the key", () => {
    expect(parseAgentAccounts(null)).toEqual({ accounts: [], error: "" });
    expect(parseAgentAccounts(undefined)).toEqual({ accounts: [], error: "" });
    expect(parseAgentAccounts({ metadata: {} })).toEqual({ accounts: [], error: "" });
    expect(parseAgentAccounts({ metadata: null })).toEqual({ accounts: [], error: "" });
  });

  it("degrades to an empty list instead of throwing on a malformed payload", () => {
    const malformed: unknown[] = [
      runtimeWith({ cli: "dsh" }),
      runtimeWith("not-an-array"),
      runtimeWith(42),
      runtimeWith([null, "entry", 7, []]),
      // Missing identity fields.
      runtimeWith([{ cli: "dsh", account: "default" }]),
      runtimeWith([{ cli: "dsh", home: "/Users/you/.dsh" }]),
      runtimeWith([{ account: "default", home: "/Users/you/.dsh" }]),
      runtimeWith([{ cli: "dsh", account: "", home: "/Users/you/.dsh" }]),
      // Unknown CLI: a server-driven enum value this client has no group for.
      runtimeWith([{ cli: "wizard", account: "default", home: "/Users/you/.wizard" }]),
      runtimeWith([{ cli: "gemini", account: "default", home: "/Users/you/.gemini" }]),
    ];

    for (const runtime of malformed) {
      expect(parseAgentAccounts(runtime as { metadata: Record<string, unknown> })).toEqual({
        accounts: [],
        error: "",
      });
    }
  });

  it("keeps the good entries of a mixed list and preserves daemon order", () => {
    const { accounts } = parseAgentAccounts(
      runtimeWith([
        { cli: "agy", account: "default", home: "/Users/you/.gemini", lever: "custom_args:--gemini_dir" },
        { cli: "wizard", account: "default", home: "/Users/you/.wizard" },
        { cli: "dsh", account: "", home: "/Users/you/.dsh" },
        { cli: "codex", account: "default", home: "/Users/you/.codex", lever: "" },
      ]),
    );

    expect(accounts.map((entry) => `${entry.cli}/${entry.account}`)).toEqual([
      "agy/default",
      "codex/default",
    ]);
  });

  it("normalises the case and padding of identity fields", () => {
    const { accounts } = parseAgentAccounts(
      runtimeWith([{ cli: " DSH ", account: " default ", home: " /Users/you/.dsh/ " }]),
    );

    expect(accounts[0]).toMatchObject({
      cli: "dsh",
      account: "default",
      home: "/Users/you/.dsh",
    });
  });

  it("drops duplicate (cli, account) pairs, keeping the first", () => {
    const { accounts } = parseAgentAccounts(
      runtimeWith([
        { cli: "dsh", account: "default", home: "/Users/you/.dsh" },
        { cli: "dsh", account: "default", home: "/Users/you/.other" },
      ]),
    );

    expect(accounts).toHaveLength(1);
    expect(accounts[0]?.home).toBe("/Users/you/.dsh");
  });

  it("coerces drifted decorative fields instead of losing the row", () => {
    const { accounts } = parseAgentAccounts(
      runtimeWith([
        {
          cli: "claude",
          account: "default",
          home: "/Users/you/.claude/",
          base_url: 42,
          lever: "env:CLAUDE_CONFIG_DIR",
          signed_in: "true",
        },
      ]),
    );

    expect(accounts[0]).toEqual({
      cli: "claude",
      account: "default",
      home: "/Users/you/.claude",
      base_url: "",
      key_ref: "",
      lever: "env:CLAUDE_CONFIG_DIR",
      // Unknown booleans read as "not confirmed signed in".
      signed_in: false,
    });
  });

  it("drops a key_ref no name could be and passes a real name through", () => {
    const { accounts } = parseAgentAccounts(
      runtimeWith([
        { cli: "dsh", account: "default", home: "/Users/you/.dsh", key_ref: "dsh-default" },
        { cli: "claude", account: "default", home: "/Users/you/.claude", key_ref: "keychain:claude-main" },
        // Whitespace, a control character and an absurd length cannot be a name.
        { cli: "codex", account: "default", home: "/Users/you/.codex", key_ref: "has space" },
        { cli: "cursor", account: "default", home: "/Users/you/.cursor", key_ref: "line\nbreak" },
        { cli: "agy", account: "default", home: "/Users/you/.gemini", key_ref: "x".repeat(129) },
      ]),
    );

    expect(accounts.map((entry) => entry.key_ref)).toEqual([
      "dsh-default",
      "keychain:claude-main",
      "",
      "",
      "",
    ]);
  });

  it("reads agent_accounts_error as one line and ignores a non-string error", () => {
    const runtime = runtimeWith(
      [],
      "EACCES: permission denied,\n  scandir '/Users/you/.dsh'",
    );
    expect(parseAgentAccounts(runtime).error).toBe(
      "EACCES: permission denied, scandir '/Users/you/.dsh'",
    );

    expect(parseAgentAccounts(runtimeWith([], 500)).error).toBe("");
    expect(parseAgentAccounts(runtimeWith([], "")).error).toBe("");
  });
});

describe("groupAccountsByCli", () => {
  const fiveClis = [
    CURSOR_DEFAULT,
    account({ cli: "dsh", account: "account10", home: `${RUNTIME_HOME}/.dsh-account10`, lever: "env:DSH_HOME" }),
    AGY_DEFAULT,
    DSH_DEFAULT,
    DSH_ACCOUNT2,
    CLAUDE_DEFAULT,
    CODEX_DEFAULT,
  ];

  it("groups in the fixed CLI order and sorts accounts inside a group", () => {
    const groups = groupAccountsByCli(fiveClis);

    expect(groups.map((group) => group.cli)).toEqual(["dsh", "agy", "codex", "claude", "cursor"]);
    expect(groups.map((group) => group.cli)).toEqual([...AGENT_ACCOUNT_CLIS]);
    expect(groups[0]?.accounts.map((entry) => entry.account)).toEqual([
      "default",
      "account2",
      "account10",
    ]);
    expect(groups[0]?.lever).toBe("env:DSH_HOME");
    expect(groups.map((group) => group.switchable)).toEqual([true, false, false, true, false]);
    expect(groups[4]?.lever).toBe("");
  });

  it("does not depend on the order the daemon reported accounts in", () => {
    const ids = (list: readonly AgentAccount[]) =>
      groupAccountsByCli(list).map(
        (group) => `${group.cli}:${group.accounts.map((entry) => entry.account).join(",")}`,
      );

    expect(ids([...fiveClis].reverse())).toEqual(ids(fiveClis));
    expect(ids([DSH_ACCOUNT2, CURSOR_DEFAULT, AGY_DEFAULT])).toEqual(ids([AGY_DEFAULT, DSH_ACCOUNT2, CURSOR_DEFAULT]));
  });

  it("keeps an older daemon's agy directories, read-only", () => {
    const [group] = groupAccountsByCli([AGY_LEGACY_ACCOUNT2, AGY_DEFAULT]);

    expect(group?.accounts.map((entry) => entry.account)).toEqual(["default", "account2"]);
    expect(group?.switchable).toBe(false);
  });

  it("omits CLIs without accounts", () => {
    expect(groupAccountsByCli([DSH_DEFAULT]).map((group) => group.cli)).toEqual(["dsh"]);
    expect(groupAccountsByCli([])).toEqual([]);
  });
});

describe("withAccountSlots", () => {
  const DSH_CUSTOM = account({
    cli: "dsh",
    account: "work",
    home: `${RUNTIME_HOME}/.dsh-work`,
    lever: "env:DSH_HOME",
  });
  const CLAUDE_DEFAULT = account({
    cli: "claude",
    account: "default",
    home: `${RUNTIME_HOME}/.claude`,
    lever: "env:CLAUDE_CONFIG_DIR",
  });
  const ids = (list: readonly AgentAccount[]) => list.map((entry) => entry.account);
  const groupIds = (list: readonly AgentAccount[], cli: string) =>
    ids(groupAccountsByCli(list).find((group) => group.cli === cli)?.accounts ?? []);

  it("keeps a reported slot row and synthesizes the ones without a directory", () => {
    const merged = withAccountSlots(
      [DSH_DEFAULT, DSH_ACCOUNT2],
      { dsh: [1, 2, 3] },
      RUNTIME_HOME,
    );

    expect(groupIds(merged, "dsh")).toEqual(["default", "account2", "account3"]);
    // A reported row keeps the daemon's own directory and sign-in state.
    expect(merged[1]).toBe(DSH_ACCOUNT2);
    const synthesized = merged.find((entry) => entry.account === "account3");
    expect(synthesized).toEqual({
      cli: "dsh",
      account: "account3",
      home: `${RUNTIME_HOME}/.dsh-account3`,
      base_url: "",
      key_ref: "",
      lever: "env:DSH_HOME",
      signed_in: false,
    });
  });

  it("synthesizes a manual family's slot with that family's directory and env lever", () => {
    const merged = withAccountSlots(
      [DSH_DEFAULT, CLAUDE_DEFAULT],
      { dsh: [1, 2], claude: [1, 3] },
      RUNTIME_HOME,
    );

    expect(merged.find((entry) => entry.cli === "dsh" && entry.account === "account2")).toEqual({
      cli: "dsh",
      account: "account2",
      home: `${RUNTIME_HOME}/.dsh-account2`,
      base_url: "",
      key_ref: "",
      lever: "env:DSH_HOME",
      signed_in: false,
    });
    expect(
      merged.find((entry) => entry.cli === "claude" && entry.account === "account3"),
    ).toMatchObject({
      home: `${RUNTIME_HOME}/.claude-account3`,
      lever: "env:CLAUDE_CONFIG_DIR",
    });
  });

  it("passes agy through as reported, whatever an old agy_slots list says", () => {
    const merged = withAccountSlots([AGY_DEFAULT, AGY_LEGACY_ACCOUNT2], { agy: [1, 3] }, RUNTIME_HOME);

    expect(merged).toEqual([AGY_DEFAULT, AGY_LEGACY_ACCOUNT2]);
  });

  it("keeps a reported directory of a manual family whatever its list says", () => {
    // `~/.dsh-account2` was switchable before slots existed. An agent that
    // never saved a dsh list must not lose it — a family's list only ever ADDS
    // rows.
    const merged = withAccountSlots([DSH_DEFAULT, DSH_ACCOUNT2], { dsh: [1] }, RUNTIME_HOME);

    expect(groupIds(merged, "dsh")).toEqual(["default", "account2"]);
    expect(merged).toContain(DSH_ACCOUNT2);
    expect(groupIds(withAccountSlots([DSH_DEFAULT, DSH_ACCOUNT2], {}, RUNTIME_HOME), "dsh")).toEqual([
      "default",
      "account2",
    ]);
  });

  it("always carries slot 1, even from an empty list", () => {
    const merged = withAccountSlots([DSH_CUSTOM], { dsh: [] }, RUNTIME_HOME);

    expect(ids(merged)).toEqual(["work", "default"]);
    expect(merged[1]?.home).toBe(`${RUNTIME_HOME}/.dsh`);
  });

  it("never invents a group for a CLI the machine did not report", () => {
    // A dsh-only host must not grow a synthesized Claude group: there is no
    // surface to edit there.
    const merged = withAccountSlots(
      [DSH_DEFAULT, CODEX_DEFAULT],
      { claude: [1, 2] },
      RUNTIME_HOME,
    );

    expect(merged).toHaveLength(2);
    expect(merged).toContain(DSH_DEFAULT);
    expect(merged).toContain(CODEX_DEFAULT);
    expect(groupAccountsByCli(merged).map((group) => group.cli)).toEqual(["dsh", "codex"]);
  });

  it("never gives a CLI without a family a slot, even when a list names it", () => {
    const merged = withAccountSlots([CODEX_DEFAULT], { codex: [1, 2] }, RUNTIME_HOME);

    expect(merged).toEqual([CODEX_DEFAULT]);
  });

  it("leaves accounts outside the numbered convention and every other CLI alone", () => {
    const merged = withAccountSlots(
      [DSH_DEFAULT, DSH_CUSTOM, AGY_DEFAULT, CODEX_DEFAULT],
      { dsh: [1, 5] },
      RUNTIME_HOME,
    );

    // The custom DSH directory and both non-dsh rows pass through untouched —
    // a custom DSH_HOME is a real directory an agent may be bound to, so
    // hiding it would leave the account in effect without a row.
    expect(merged).toContain(DSH_CUSTOM);
    expect(merged).toContain(AGY_DEFAULT);
    expect(merged).toContain(CODEX_DEFAULT);
    expect(merged).toHaveLength(5);
    // Inside the group `default` leads and the numbered slot sorts before the
    // named custom directory, exactly as the group sort already did.
    expect(groupIds(merged, "dsh")).toEqual(["default", "account5", "work"]);
  });

  it("keeps a synthesized directory relative when the host home is unknown", () => {
    const merged = withAccountSlots([DSH_DEFAULT], { dsh: [1, 2] }, null);

    // `planAccountSwitch` refuses a relative home, so an unresolved host home
    // ends as "this account cannot be bound" rather than a guessed path.
    expect(merged.find((entry) => entry.account === "account2")?.home).toBe(
      ".dsh-account2",
    );
  });

  it("names the slot directories the same way the daemon does", () => {
    expect(slotAccountId(1)).toBe("default");
    expect(slotAccountId(2)).toBe("account2");
    expect(accountSlotNumberOf(DSH_DEFAULT)).toBe(1);
    expect(accountSlotNumberOf(DSH_ACCOUNT2)).toBe(2);
    expect(accountSlotNumberOf(DSH_CUSTOM)).toBeNull();
    expect(accountSlotNumberOf(AGY_LEGACY_ACCOUNT2)).toBeNull();
    // No family, no slot — whatever the directory happens to be called.
    expect(accountSlotNumberOf(CODEX_DEFAULT)).toBeNull();
    expect(accountSlotNumberOf({ cli: "codex", account: "account2" })).toBeNull();
  });
});

describe("nextSlotForGroup", () => {
  const groupOf = (accounts: AgentAccount[]) => groupAccountsByCli(accounts)[0]!;

  it("offers the first free number to every family with a writable lever", () => {
    expect(nextSlotForGroup(groupOf([DSH_DEFAULT]), { dsh: [1, 2, 3] })).toBe(4);
    expect(nextSlotForGroup(groupOf([CLAUDE_DEFAULT]), { claude: [1] })).toBe(2);
    expect(nextSlotForGroup(groupOf([DSH_DEFAULT]), { dsh: [1] })).toBe(2);
    expect(nextSlotForGroup(groupOf([DSH_DEFAULT]), {})).toBe(2);
  });

  it("skips a number whose directory is already listed in a manual family", () => {
    expect(nextSlotForGroup(groupOf([DSH_DEFAULT, DSH_ACCOUNT2]), { dsh: [1] })).toBe(3);
  });

  it("offers nothing to a CLI without a family, whatever lever it reports", () => {
    expect(nextSlotForGroup(groupOf([CODEX_DEFAULT]), {})).toBeNull();
    expect(nextSlotForGroup(groupOf([AGY_DEFAULT]), { agy: [1, 2] })).toBeNull();
    // Even a future daemon advertising a lever cannot conjure a registry: the
    // family table is what says a bound slot reaches the task.
    const levered = account({
      cli: "codex",
      account: "default",
      home: `${RUNTIME_HOME}/.codex`,
      lever: "env:CODEX_HOME",
    });
    expect(nextSlotForGroup(groupOf([levered]), {})).toBeNull();
  });

  it("offers nothing when the daemon reported the family's lever as unwritable", () => {
    const readonly = account({
      cli: "dsh",
      account: "default",
      home: `${RUNTIME_HOME}/.dsh`,
      lever: "",
    });
    expect(nextSlotForGroup(groupOf([readonly]), { dsh: [1] })).toBeNull();
  });

  it("offers nothing once the family is full", () => {
    const full = Array.from({ length: 32 }, (_, index) => index + 1);
    expect(nextSlotForGroup(groupOf([DSH_DEFAULT]), { dsh: full })).toBeNull();
  });
});

describe("removableSlots", () => {
  it("only drops a slot that exists nowhere but in the list", () => {
    // account2 is on disk: the daemon keeps reporting it, so a drop would be a
    // control that does nothing.
    expect(removableSlots([DSH_DEFAULT, DSH_ACCOUNT2], { dsh: [1, 2, 3] }).dsh).toEqual([3]);
  });

  it("never offers slot 1 or a CLI without a family", () => {
    const result = removableSlots([DSH_DEFAULT, CODEX_DEFAULT], { dsh: [1], codex: [1, 2] });
    expect(result.dsh).toEqual([]);
    expect(result.codex).toBeUndefined();
  });
});

describe("boundDirectoryFor", () => {
  it("reads each family's binding from the place its lever writes", () => {
    const binding = {
      custom_env: { DSH_HOME: " /Users/you/.dsh-account3 " },
    };
    expect(accountSlotFamily("agy")).toBeNull();
    expect(boundDirectoryFor(binding, accountSlotFamily("dsh")!)).toBe(
      "/Users/you/.dsh-account3",
    );
    expect(boundDirectoryFor(binding, accountSlotFamily("claude")!)).toBe("");
    expect(boundDirectoryFor(null, accountSlotFamily("dsh")!)).toBe("");
  });
});

describe("lever vocabulary", () => {
  it("parses the contract levers and rejects unknown shapes", () => {
    expect(parseAccountLever("env:DSH_HOME")).toEqual({ kind: "env", key: "DSH_HOME" });
    expect(parseAccountLever("")).toEqual({ kind: "none" });
    expect(parseAccountLever(null)).toEqual({ kind: "none" });
    expect(parseAccountLever("env:not a key")).toEqual({ kind: "unknown" });
    expect(parseAccountLever("fs:/tmp/account")).toEqual({ kind: "unknown" });
    // agy's retired lever, as a 0.6.x daemon still reports it.
    expect(parseAccountLever("custom_args:--gemini_dir")).toEqual({ kind: "unknown" });
  });

  it("marks only writable levers as switchable", () => {
    expect(isSwitchableLever("env:DSH_HOME")).toBe(true);
    expect(isSwitchableLever("")).toBe(false);
    expect(isSwitchableLever("custom_args:--gemini_dir")).toBe(false);
  });

  it("labels group headers with the lever the user must understand", () => {
    expect(accountLeverLabel("env:DSH_HOME")).toBe("DSH_HOME");
    expect(accountLeverLabel("")).toBe("");
    expect(accountLeverLabel("fs:/tmp/account")).toBe("fs:/tmp/account");
  });

  it("maps runtime providers onto the CLI whose accounts they consume", () => {
    expect(cliForProvider("antigravity")).toBe("agy");
    expect(cliForProvider("DSH")).toBe("dsh");
    expect(cliForProvider("claude")).toBe("claude");
    expect(cliForProvider("opencode")).toBeNull();
    expect(cliForProvider(null)).toBeNull();
  });
});

describe("resolveCurrentAccount", () => {
  const dshAccounts = [DSH_DEFAULT, DSH_ACCOUNT2];

  it("expands a ~ binding with the runtime host home", () => {
    expect(
      resolveCurrentAccount(
        { custom_env: { DSH_HOME: "~/.dsh-account2" }, runtime_home: RUNTIME_HOME },
        dshAccounts,
      ),
    ).toEqual(DSH_ACCOUNT2);
  });

  it("falls back to the CLI's default account while the lever is unbound", () => {
    expect(resolveCurrentAccount({ custom_env: {}, provider: "dsh" }, dshAccounts)).toEqual(
      DSH_DEFAULT,
    );
  });

  it("describes agy by its own directory, even next to an older daemon's numbered one", () => {
    expect(
      resolveCurrentAccount({ provider: "antigravity" }, [AGY_DEFAULT, AGY_LEGACY_ACCOUNT2]),
    ).toEqual(AGY_DEFAULT);
  });

  it("returns null when the bound directory belongs to no reported account", () => {
    expect(
      resolveCurrentAccount({ custom_env: { DSH_HOME: "/opt/other/.dsh" }, provider: "dsh" }, dshAccounts),
    ).toBeNull();
  });

  it("reads env levers out of the agent's env map", () => {
    expect(
      resolveCurrentAccount({ custom_env: { DSH_HOME: DSH_ACCOUNT2.home }, provider: "dsh" }, dshAccounts),
    ).toEqual(DSH_ACCOUNT2);
  });

  it("scopes to the runtime's CLI, so another CLI's binding cannot win", () => {
    const accounts = [DSH_DEFAULT, DSH_ACCOUNT2, CLAUDE_DEFAULT, CLAUDE_ACCOUNT2];
    const agent = {
      provider: "claude",
      custom_env: { DSH_HOME: DSH_ACCOUNT2.home, CLAUDE_CONFIG_DIR: CLAUDE_ACCOUNT2.home },
    };

    expect(resolveCurrentAccount(agent, accounts)).toEqual(CLAUDE_ACCOUNT2);
    // Without a readable provider the explicit binding still decides, even
    // though dsh leads the canonical group order.
    expect(
      resolveCurrentAccount({ custom_env: { CLAUDE_CONFIG_DIR: CLAUDE_ACCOUNT2.home } }, accounts),
    ).toEqual(CLAUDE_ACCOUNT2);
  });

  it("returns null when there are no accounts at all", () => {
    expect(resolveCurrentAccount({ provider: "antigravity" }, [])).toBeNull();
  });
});

describe("accountStatus", () => {
  it("maps signed-in and signed-out accounts", () => {
    expect(accountStatus(account({ cli: "dsh", account: "default", home: "/h", signed_in: true }))).toEqual(
      { kind: "signed_in" },
    );
    expect(accountStatus(account({ cli: "dsh", account: "default", home: "/h", signed_in: false }))).toEqual(
      { kind: "signed_out" },
    );
  });
});

describe("accountsViewState", () => {
  it("picks loading, error, empty or ready in that precedence", () => {
    expect(accountsViewState({ accounts: [], error: "read failed", loading: true })).toEqual({
      kind: "loading",
    });
    expect(accountsViewState({ accounts: [], error: "read failed" })).toEqual({
      kind: "error",
      message: "read failed",
    });
    // An error must never be reported as "this agent has no accounts".
    expect(accountsViewState({ accounts: [], error: "read failed" }).kind).not.toBe("empty");
    expect(accountsViewState({ accounts: [] })).toEqual({ kind: "empty" });
    expect(accountsViewState({ accounts: [DSH_DEFAULT] })).toEqual({ kind: "ready" });
    expect(accountsViewState({ accounts: [DSH_DEFAULT], error: "" })).toEqual({ kind: "ready" });
    expect(accountsViewState({ accounts: [DSH_DEFAULT], error: null })).toEqual({ kind: "ready" });
  });

  it("opens the manage drawer only for a trustworthy, non-empty list", () => {
    expect(canManageAccounts({ kind: "ready" })).toBe(true);
    expect(canManageAccounts({ kind: "loading" })).toBe(false);
    expect(canManageAccounts({ kind: "empty" })).toBe(false);
    expect(canManageAccounts({ kind: "error", message: "read failed" })).toBe(false);
  });

  it("reports a failed daemon probe as error, never as the empty state", () => {
    const parsed = parseAgentAccounts(runtimeWith([], "EACCES: permission denied"));

    expect(parsed.accounts).toEqual([]);
    expect(accountsViewState(parsed)).toEqual({
      kind: "error",
      message: "EACCES: permission denied",
    });
  });
});

describe("planAccountSwitch", () => {
  it("writes an env lever as a single key so the caller can leave other keys alone", () => {
    const plan = planAccountSwitch(
      { custom_env: { DSH_HOME: DSH_DEFAULT.home, OTHER_KEY: "keep-me" } },
      DSH_ACCOUNT2,
      READY,
    );

    expect(plan).toEqual({ kind: "env", key: "DSH_HOME", value: DSH_ACCOUNT2.home });
  });

  it("reports an empty lever as unsupported instead of offering a dead button", () => {
    expect(planAccountSwitch({}, CODEX_DEFAULT, READY)).toEqual({
      kind: "unsupported",
      reason: "no_lever",
    });
    expect(planAccountSwitch({}, CURSOR_DEFAULT, READY)).toEqual({
      kind: "unsupported",
      reason: "no_lever",
    });
    expect(planAccountSwitch({}, AGY_DEFAULT, READY)).toEqual({
      kind: "unsupported",
      reason: "no_lever",
    });
  });

  it("reports a lever shape this client cannot write as unsupported", () => {
    const futureFlag = account({
      cli: "dsh",
      account: "account2",
      home: DSH_ACCOUNT2.home,
      lever: "custom_args:--dsh_profile",
    });
    const futureScheme = account({
      cli: "dsh",
      account: "account2",
      home: DSH_ACCOUNT2.home,
      lever: "fs:/tmp/accounts",
    });

    expect(planAccountSwitch({}, futureFlag, READY)).toEqual({
      kind: "unsupported",
      reason: "unsupported_lever",
    });
    expect(planAccountSwitch({}, futureScheme, READY)).toEqual({
      kind: "unsupported",
      reason: "unsupported_lever",
    });
    // A 0.6.x daemon still reports agy's retired `--gemini_dir` lever; this
    // client no longer writes it.
    expect(planAccountSwitch({}, AGY_LEGACY_ACCOUNT2, READY)).toEqual({
      kind: "unsupported",
      reason: "unsupported_lever",
    });
  });

  it("does not write when the target is already the account in effect", () => {
    expect(
      planAccountSwitch({ custom_env: { DSH_HOME: DSH_ACCOUNT2.home } }, DSH_ACCOUNT2, READY),
    ).toEqual({ kind: "noop" });
    // Unbound lever: the CLI's own default directory is already in effect.
    expect(planAccountSwitch({ custom_env: {} }, DSH_DEFAULT, READY)).toEqual({ kind: "noop" });
  });

  it("never produces a write from loading, empty or error states", () => {
    const target = DSH_ACCOUNT2;

    for (const view of [
      { kind: "loading" },
      { kind: "empty" },
      { kind: "error", message: "EACCES: permission denied" },
    ] satisfies AccountsViewState[]) {
      expect(planAccountSwitch({ custom_env: {} }, target, view)).toEqual({
        kind: "unsupported",
        reason: "view_not_ready",
      });
    }

    // Control: the same target does write once the list is trustworthy.
    expect(planAccountSwitch({ custom_env: {} }, target, READY).kind).toBe("env");
    expect(planAccountSwitch({ custom_env: {} }, target)).toEqual({
      kind: "env",
      key: "DSH_HOME",
      value: target.home,
    });
  });

  it("requires a selected target", () => {
    expect(planAccountSwitch({ custom_env: {} }, null, READY)).toEqual({
      kind: "unsupported",
      reason: "no_target",
    });
    expect(planAccountSwitch({ custom_env: {} }, undefined, READY)).toEqual({
      kind: "unsupported",
      reason: "no_target",
    });
  });

  it("refuses to write a home that is not an absolute path", () => {
    const relative = account({
      cli: "dsh",
      account: "account2",
      home: "relative/.dsh-account2",
      lever: "env:DSH_HOME",
    });

    expect(planAccountSwitch({ custom_env: {} }, relative, READY)).toEqual({
      kind: "unsupported",
      reason: "invalid_home",
    });
  });

  it("carries no credential material into the write", () => {
    const secret = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----";
    const { accounts } = parseAgentAccounts(
      runtimeWith([
        {
          cli: "dsh",
          account: "account2",
          home: DSH_ACCOUNT2.home,
          key_ref: secret,
          lever: "env:DSH_HOME",
        },
      ]),
    );
    const target = accounts[0];
    expect(target?.key_ref).toBe("");

    const plan = planAccountSwitch({ custom_env: {} }, target, READY);
    expect(JSON.stringify(plan)).not.toContain("PRIVATE KEY");
    expect(JSON.stringify(plan)).not.toContain("key_ref");
  });
});
