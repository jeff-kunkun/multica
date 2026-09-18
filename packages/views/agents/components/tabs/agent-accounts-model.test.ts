// @vitest-environment node
//
// Canonical test layer for the accounts tab's rules: parsing, grouping, status
// mapping, the four page states and the switch plan (DENE-307). These are pure
// functions with no DOM, so this suite runs under node and the DENE-308
// component suite must NOT re-run this matrix through a mount — it points here.

import { describe, expect, it } from "vitest";
import {
  AGENT_ACCOUNT_CLIS,
  type AccountsViewState,
  type AgentAccount,
  type AgentAccountBinding,
  accountLeverLabel,
  accountStatus,
  accountsViewState,
  agySlotAccountId,
  agySlotNumberOf,
  canManageAccounts,
  cliForProvider,
  formatQuotaResetAt,
  groupAccountsByCli,
  isSwitchableLever,
  nextQuotaResetMs,
  parseAccountLever,
  parseAgentAccounts,
  planAccountSwitch,
  quotaSwitchCandidate,
  resolveCurrentAccount,
  withAgySlots,
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
    quota_reset_at: 0,
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

const AGY_DEFAULT = account({
  cli: "agy",
  account: "default",
  home: `${RUNTIME_HOME}/.gemini`,
  lever: "custom_args:--gemini_dir",
});
const AGY_ACCOUNT2 = account({
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
const CODEX_ACCOUNT2 = account({
  cli: "codex",
  account: "account2",
  home: `${RUNTIME_HOME}/.codex-account2`,
  lever: "",
});
const CLAUDE_DEFAULT = account({
  cli: "claude",
  account: "default",
  home: `${RUNTIME_HOME}/.claude`,
  lever: "env:CLAUDE_CONFIG_DIR",
});
const CURSOR_DEFAULT = account({
  cli: "cursor",
  account: "default",
  home: `${RUNTIME_HOME}/.cursor`,
  lever: "",
});
const CURSOR_ACCOUNT2 = account({
  cli: "cursor",
  account: "account2",
  home: `${RUNTIME_HOME}/.cursor-account2`,
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
        quota_reset_at: 0,
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
          quota_reset_at: 0,
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
          quota_reset_at: "1700000000",
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
      // Unknown booleans read as "not confirmed signed in"; a stringified unix
      // timestamp is still a timestamp.
      signed_in: false,
      quota_reset_at: 1700000000,
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
    account({ cli: "agy", account: "account10", home: `${RUNTIME_HOME}/.gemini-account10`, lever: "custom_args:--gemini_dir" }),
    AGY_DEFAULT,
    DSH_DEFAULT,
    account({ cli: "agy", account: "account2", home: `${RUNTIME_HOME}/.gemini-account2`, lever: "custom_args:--gemini_dir" }),
    CLAUDE_DEFAULT,
    CODEX_DEFAULT,
  ];

  it("groups in the fixed CLI order and sorts accounts inside a group", () => {
    const groups = groupAccountsByCli(fiveClis);

    expect(groups.map((group) => group.cli)).toEqual(["dsh", "agy", "codex", "claude", "cursor"]);
    expect(groups.map((group) => group.cli)).toEqual([...AGENT_ACCOUNT_CLIS]);
    expect(groups[1]?.accounts.map((entry) => entry.account)).toEqual([
      "default",
      "account2",
      "account10",
    ]);
    expect(groups[1]?.lever).toBe("custom_args:--gemini_dir");
    expect(groups.map((group) => group.switchable)).toEqual([true, true, false, true, false]);
    expect(groups[4]?.lever).toBe("");
  });

  it("does not depend on the order the daemon reported accounts in", () => {
    const ids = (list: readonly AgentAccount[]) =>
      groupAccountsByCli(list).map(
        (group) => `${group.cli}:${group.accounts.map((entry) => entry.account).join(",")}`,
      );

    expect(ids([...fiveClis].reverse())).toEqual(ids(fiveClis));
    expect(ids([AGY_ACCOUNT2, CURSOR_DEFAULT, DSH_DEFAULT])).toEqual(ids([DSH_DEFAULT, AGY_ACCOUNT2, CURSOR_DEFAULT]));
  });

  it("omits CLIs without accounts", () => {
    expect(groupAccountsByCli([DSH_DEFAULT]).map((group) => group.cli)).toEqual(["dsh"]);
    expect(groupAccountsByCli([])).toEqual([]);
  });
});

describe("withAgySlots", () => {
  const AGY_CUSTOM = account({
    cli: "agy",
    account: "work",
    home: `${RUNTIME_HOME}/.gemini-work`,
    lever: "custom_args:--gemini_dir",
  });
  const ids = (list: readonly AgentAccount[]) => list.map((entry) => entry.account);

  it("keeps a reported slot row and synthesizes the ones without a directory", () => {
    const merged = withAgySlots([AGY_DEFAULT, AGY_ACCOUNT2], [1, 2, 3], RUNTIME_HOME);

    expect(ids(groupAccountsByCli(merged)[0]!.accounts)).toEqual([
      "default",
      "account2",
      "account3",
    ]);
    // A reported row keeps the daemon's own directory and sign-in state.
    expect(merged[1]).toBe(AGY_ACCOUNT2);
    const synthesized = merged.find((entry) => entry.account === "account3");
    expect(synthesized).toEqual({
      cli: "agy",
      account: "account3",
      home: `${RUNTIME_HOME}/.gemini-account3`,
      base_url: "",
      key_ref: "",
      lever: "custom_args:--gemini_dir",
      signed_in: false,
      quota_reset_at: 0,
    });
  });

  it("drops a numbered directory the agent's slot list does not carry", () => {
    const merged = withAgySlots([AGY_DEFAULT, AGY_ACCOUNT2], [1], RUNTIME_HOME);

    expect(ids(merged)).toEqual(["default"]);
  });

  it("always carries slot 1, even from an empty list", () => {
    const merged = withAgySlots([AGY_CUSTOM], [], RUNTIME_HOME);

    expect(ids(merged)).toEqual(["work", "default"]);
    expect(merged[1]?.home).toBe(`${RUNTIME_HOME}/.gemini`);
  });

  it("leaves the report untouched when the machine has no AGY directory", () => {
    // A dsh-only host must not grow a synthesized AGY group: there is no AGY
    // surface to edit, and the drawer's add button has to stay a description.
    const merged = withAgySlots(
      [DSH_DEFAULT, CODEX_DEFAULT],
      [1, 2, 3],
      RUNTIME_HOME,
    );

    expect(merged).toEqual([DSH_DEFAULT, CODEX_DEFAULT]);
    expect(groupAccountsByCli(merged).some((group) => group.cli === "agy")).toBe(
      false,
    );
  });

  it("leaves accounts outside the numbered convention and every other CLI alone", () => {
    const merged = withAgySlots(
      [AGY_DEFAULT, AGY_CUSTOM, DSH_DEFAULT, CODEX_DEFAULT],
      [1, 5],
      RUNTIME_HOME,
    );

    // The custom AGY directory and both non-agy rows pass through untouched —
    // a custom `--gemini_dir` is a real directory an agent may be bound to, so
    // hiding it would leave the account in effect without a row.
    expect(merged).toContain(AGY_CUSTOM);
    expect(merged).toContain(DSH_DEFAULT);
    expect(merged).toContain(CODEX_DEFAULT);
    expect(ids(merged)).toEqual(["work", "default", "default", "default", "account5"]);
    // Inside the group `default` leads and the numbered slot sorts before the
    // named custom directory, exactly as the group sort already did.
    expect(ids(groupAccountsByCli(merged).find((group) => group.cli === "agy")!.accounts)).toEqual([
      "default",
      "account5",
      "work",
    ]);
  });

  it("keeps a synthesized directory relative when the host home is unknown", () => {
    const merged = withAgySlots([AGY_DEFAULT], [1, 2], null);

    // `planAccountSwitch` refuses a relative home, so an unresolved host home
    // ends as "this account cannot be bound" rather than a guessed path.
    expect(merged.find((entry) => entry.account === "account2")?.home).toBe(
      ".gemini-account2",
    );
  });

  it("names the slot directories the same way the daemon does", () => {
    expect(agySlotAccountId(1)).toBe("default");
    expect(agySlotAccountId(2)).toBe("account2");
    expect(agySlotNumberOf(AGY_DEFAULT)).toBe(1);
    expect(agySlotNumberOf(AGY_ACCOUNT2)).toBe(2);
    expect(agySlotNumberOf(AGY_CUSTOM)).toBeNull();
    expect(agySlotNumberOf(DSH_DEFAULT)).toBeNull();
  });
});

describe("lever vocabulary", () => {
  it("parses the contract levers and rejects unknown shapes", () => {
    expect(parseAccountLever("custom_args:--gemini_dir")).toEqual({
      kind: "custom_args",
      flag: "--gemini_dir",
    });
    expect(parseAccountLever("env:DSH_HOME")).toEqual({ kind: "env", key: "DSH_HOME" });
    expect(parseAccountLever("")).toEqual({ kind: "none" });
    expect(parseAccountLever(null)).toEqual({ kind: "none" });
    expect(parseAccountLever("env:not a key")).toEqual({ kind: "unknown" });
    expect(parseAccountLever("fs:/tmp/account")).toEqual({ kind: "unknown" });
  });

  it("marks only writable levers as switchable", () => {
    expect(isSwitchableLever("env:DSH_HOME")).toBe(true);
    expect(isSwitchableLever("custom_args:--gemini_dir")).toBe(true);
    expect(isSwitchableLever("")).toBe(false);
    // A newer daemon's flag is not something this client can write.
    expect(isSwitchableLever("custom_args:--gemini_profile")).toBe(false);
  });

  it("labels group headers with the lever the user must understand", () => {
    expect(accountLeverLabel("env:DSH_HOME")).toBe("DSH_HOME");
    expect(accountLeverLabel("custom_args:--gemini_dir")).toBe("--gemini_dir");
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
  const agyAccounts = [AGY_DEFAULT, AGY_ACCOUNT2];

  it("reads agy's binding out of --gemini_dir, in both argument forms", () => {
    expect(
      resolveCurrentAccount(
        { custom_args: ["--gemini_dir", AGY_ACCOUNT2.home], provider: "antigravity" },
        agyAccounts,
      ),
    ).toEqual(AGY_ACCOUNT2);
    expect(
      resolveCurrentAccount(
        { custom_args: [`--gemini_dir=${AGY_ACCOUNT2.home}`], provider: "antigravity" },
        agyAccounts,
      ),
    ).toEqual(AGY_ACCOUNT2);
  });

  it("expands a ~ binding with the runtime host home", () => {
    expect(
      resolveCurrentAccount(
        { custom_args: ["--gemini_dir", "~/.gemini-account2"], runtime_home: RUNTIME_HOME },
        agyAccounts,
      ),
    ).toEqual(AGY_ACCOUNT2);
  });

  it("falls back to the CLI's default account while the lever is unbound", () => {
    expect(resolveCurrentAccount({ custom_args: [], provider: "antigravity" }, agyAccounts)).toEqual(
      AGY_DEFAULT,
    );
    expect(resolveCurrentAccount({ custom_env: {}, provider: "dsh" }, [DSH_DEFAULT, DSH_ACCOUNT2])).toEqual(
      DSH_DEFAULT,
    );
  });

  it("returns null when the bound directory belongs to no reported account", () => {
    expect(
      resolveCurrentAccount(
        { custom_args: ["--gemini_dir", "/opt/other/.gemini"], provider: "antigravity" },
        agyAccounts,
      ),
    ).toBeNull();
    expect(
      resolveCurrentAccount({ custom_env: { DSH_HOME: "/opt/other/.dsh" }, provider: "dsh" }, [
        DSH_DEFAULT,
        DSH_ACCOUNT2,
      ]),
    ).toBeNull();
  });

  it("reads env levers out of the agent's env map", () => {
    expect(
      resolveCurrentAccount({ custom_env: { DSH_HOME: DSH_ACCOUNT2.home }, provider: "dsh" }, [
        DSH_DEFAULT,
        DSH_ACCOUNT2,
      ]),
    ).toEqual(DSH_ACCOUNT2);
  });

  it("scopes to the runtime's CLI, so another CLI's binding cannot win", () => {
    const accounts = [AGY_DEFAULT, AGY_ACCOUNT2, DSH_DEFAULT, DSH_ACCOUNT2];
    const agent = {
      provider: "antigravity",
      custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
      custom_env: { DSH_HOME: DSH_ACCOUNT2.home },
    };

    expect(resolveCurrentAccount(agent, accounts)).toEqual(AGY_ACCOUNT2);
    // Without a readable provider the explicit binding still decides, even
    // though dsh leads the canonical group order.
    expect(
      resolveCurrentAccount({ custom_args: ["--gemini_dir", AGY_ACCOUNT2.home] }, accounts),
    ).toEqual(AGY_ACCOUNT2);
  });

  it("returns null when there are no accounts at all", () => {
    expect(resolveCurrentAccount({ provider: "antigravity" }, [])).toBeNull();
  });
});

describe("accountStatus", () => {
  const resetAt = 1_700_000_000;

  it("maps signed-in and signed-out accounts", () => {
    expect(accountStatus(account({ cli: "dsh", account: "default", home: "/h", signed_in: true }))).toEqual(
      { kind: "signed_in" },
    );
    expect(accountStatus(account({ cli: "dsh", account: "default", home: "/h", signed_in: false }))).toEqual(
      { kind: "signed_out" },
    );
  });

  it("carries the reset moment while a quota is exhausted", () => {
    const exhausted = account({
      cli: "agy",
      account: "account2",
      home: "/h",
      signed_in: true,
      quota_reset_at: resetAt,
    });

    expect(accountStatus(exhausted, resetAt * 1000 - 1)).toEqual({
      kind: "quota_exhausted",
      reset_at: resetAt,
      reset_at_ms: resetAt * 1000,
    });
    // Once the reset moment has passed the account is usable again.
    expect(accountStatus(exhausted, resetAt * 1000)).toEqual({ kind: "signed_in" });
    expect(accountStatus(exhausted, resetAt * 1000 + 60_000)).toEqual({ kind: "signed_in" });
  });

  it("prefers signed_out over an exhausted quota: without credentials there is nothing to wait for", () => {
    expect(
      accountStatus(
        account({ cli: "agy", account: "account2", home: "/h", signed_in: false, quota_reset_at: resetAt }),
        resetAt * 1000 - 1,
      ),
    ).toEqual({ kind: "signed_out" });
  });
});

describe("nextQuotaResetMs", () => {
  const resetAt = 1_700_000_000;
  const later = resetAt + 3_600;
  const exhausted = (id: string, at: number) =>
    account({
      cli: "agy",
      account: id,
      home: `${RUNTIME_HOME}/.gemini`,
      quota_reset_at: at,
    });

  it("returns the soonest deadline among the accounts still exhausted", () => {
    const accounts = [
      exhausted("default", later),
      exhausted("account2", resetAt),
      DSH_DEFAULT,
    ];

    expect(nextQuotaResetMs(accounts, resetAt * 1000 - 1)).toBe(resetAt * 1000);
    // The first deadline passing promotes the next one, so the surfaces keep
    // ticking until every spent quota is back.
    expect(nextQuotaResetMs(accounts, resetAt * 1000)).toBe(later * 1000);
    expect(nextQuotaResetMs(accounts, later * 1000)).toBeNull();
  });

  it("ignores accounts with nothing to wait for", () => {
    expect(nextQuotaResetMs([], resetAt * 1000)).toBeNull();
    // Signed in with an unspent quota, and signed out with a stale deadline.
    expect(nextQuotaResetMs([DSH_DEFAULT], resetAt * 1000)).toBeNull();
    expect(
      nextQuotaResetMs(
        [
          account({
            cli: "agy",
            account: "account2",
            home: `${RUNTIME_HOME}/.gemini-account2`,
            signed_in: false,
            quota_reset_at: later,
          }),
        ],
        resetAt * 1000,
      ),
    ).toBeNull();
  });
});

describe("formatQuotaResetAt", () => {
  it("renders the deadline the same way for every surface that shows it", () => {
    const at = 1_700_000_000_000;

    expect(formatQuotaResetAt(at)).toBe(
      new Date(at).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      }),
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
  it("writes agy's binding back into custom_args, preserving other arguments", () => {
    const plan = planAccountSwitch(
      { custom_args: ["--verbose", "--gemini_dir", AGY_DEFAULT.home, "--model", "fast"] },
      AGY_ACCOUNT2,
      READY,
    );

    expect(plan).toEqual({
      kind: "custom_args",
      custom_args: ["--verbose", "--model", "fast", "--gemini_dir", AGY_ACCOUNT2.home],
    });
  });

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
  });

  it("reports a lever shape this client cannot write as unsupported", () => {
    const futureFlag = account({
      cli: "agy",
      account: "account2",
      home: AGY_ACCOUNT2.home,
      lever: "custom_args:--gemini_profile",
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
  });

  it("does not write when the target is already the account in effect", () => {
    expect(
      planAccountSwitch({ custom_args: ["--gemini_dir", AGY_ACCOUNT2.home] }, AGY_ACCOUNT2, READY),
    ).toEqual({ kind: "noop" });
    // Unbound lever: the CLI's own default directory is already in effect.
    expect(planAccountSwitch({ custom_env: {} }, DSH_DEFAULT, READY)).toEqual({ kind: "noop" });
  });

  it("never produces a write from loading, empty or error states", () => {
    const target = AGY_ACCOUNT2;

    for (const view of [
      { kind: "loading" },
      { kind: "empty" },
      { kind: "error", message: "EACCES: permission denied" },
    ] satisfies AccountsViewState[]) {
      expect(planAccountSwitch({ custom_args: [] }, target, view)).toEqual({
        kind: "unsupported",
        reason: "view_not_ready",
      });
    }

    // Control: the same target does write once the list is trustworthy.
    expect(planAccountSwitch({ custom_args: [] }, target, READY).kind).toBe("custom_args");
    expect(planAccountSwitch({ custom_args: [] }, target)).toEqual({
      kind: "custom_args",
      custom_args: ["--gemini_dir", target.home],
    });
  });

  it("requires a selected target", () => {
    expect(planAccountSwitch({ custom_args: [] }, null, READY)).toEqual({
      kind: "unsupported",
      reason: "no_target",
    });
    expect(planAccountSwitch({ custom_args: [] }, undefined, READY)).toEqual({
      kind: "unsupported",
      reason: "no_target",
    });
  });

  it("refuses to write a home that is not an absolute path", () => {
    const relative = account({
      cli: "agy",
      account: "account2",
      home: "relative/.gemini-account2",
      lever: "custom_args:--gemini_dir",
    });

    expect(planAccountSwitch({ custom_args: [] }, relative, READY)).toEqual({
      kind: "unsupported",
      reason: "invalid_home",
    });
  });

  it("carries no credential material into the write", () => {
    const secret = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----";
    const { accounts } = parseAgentAccounts(
      runtimeWith([
        {
          cli: "agy",
          account: "account2",
          home: AGY_ACCOUNT2.home,
          key_ref: secret,
          lever: "custom_args:--gemini_dir",
        },
      ]),
    );
    const target = accounts[0];
    expect(target?.key_ref).toBe("");

    const plan = planAccountSwitch({ custom_args: [] }, target, READY);
    expect(JSON.stringify(plan)).not.toContain("PRIVATE KEY");
    expect(JSON.stringify(plan)).not.toContain("key_ref");
  });
});

// The one-click switch (DENE-468). This is the canonical matrix for "may the
// summary bar offer it, and which account does it move to"; the component
// suite only checks that the button renders and performs the write.
describe("quotaSwitchCandidate", () => {
  const resetAt = 1_700_000_000;
  const nowMs = resetAt * 1000 - 60_000;
  const spent = (account: AgentAccount): AgentAccount => ({
    ...account,
    quota_reset_at: resetAt,
  });
  const signedOut = (account: AgentAccount): AgentAccount => ({
    ...account,
    signed_in: false,
  });
  const pick = (input: {
    binding?: AgentAccountBinding;
    accounts: readonly AgentAccount[];
    current: AgentAccount | null;
    view?: AccountsViewState;
  }) =>
    quotaSwitchCandidate({
      binding: input.binding ?? { custom_args: [] },
      accounts: input.accounts,
      current: input.current,
      view: input.view ?? READY,
      nowMs,
    });

  it("picks a signed-in sibling of the same CLI while the current one is spent", () => {
    const candidate = pick({
      accounts: [spent(AGY_DEFAULT), AGY_ACCOUNT2, DSH_DEFAULT],
      current: spent(AGY_DEFAULT),
    });

    expect(candidate).toEqual(AGY_ACCOUNT2);
    // The button is only worth rendering because this very plan writes: it is
    // the same gate "save and switch" goes through.
    expect(planAccountSwitch({ custom_args: [] }, candidate, READY)).toEqual({
      kind: "custom_args",
      custom_args: ["--gemini_dir", AGY_ACCOUNT2.home],
    });
  });

  it("picks the single env-lever sibling the same way", () => {
    const candidate = pick({
      binding: { custom_env: {} },
      accounts: [spent(DSH_DEFAULT), DSH_ACCOUNT2],
      current: spent(DSH_DEFAULT),
    });

    expect(candidate).toEqual(DSH_ACCOUNT2);
    expect(planAccountSwitch({ custom_env: {} }, candidate, READY)).toEqual({
      kind: "env",
      key: "DSH_HOME",
      value: DSH_ACCOUNT2.home,
    });
  });

  it("is deterministic and independent of the order the daemon reported", () => {
    const accounts = [AGY_DEFAULT, AGY_ACCOUNT2, DSH_DEFAULT];
    const reversed = [...accounts].reverse();
    const current = spent(AGY_DEFAULT);

    // `default` leads the canonical group order, so account2 is the first
    // eligible sibling under the CLI's own directory.
    expect(pick({ accounts, current })?.account).toBe("account2");
    expect(pick({ accounts: reversed, current })?.account).toBe("account2");
  });

  it("offers nothing while the account in effect has quota left", () => {
    // The action exists for one situation only; anything else stays a
    // deliberate choice made in the drawer.
    expect(pick({ accounts: [AGY_DEFAULT, AGY_ACCOUNT2], current: AGY_DEFAULT })).toBeNull();
    // Control: the same list does offer the switch while the quota is spent.
    expect(
      pick({ accounts: [spent(AGY_DEFAULT), AGY_ACCOUNT2], current: spent(AGY_DEFAULT) }),
    ).toEqual(AGY_ACCOUNT2);
  });

  it("stops offering it once the deadline has passed", () => {
    const accounts = [spent(AGY_DEFAULT), AGY_ACCOUNT2];

    expect(pick({ accounts, current: spent(AGY_DEFAULT) })).toEqual(AGY_ACCOUNT2);
    expect(
      quotaSwitchCandidate({
        binding: { custom_args: [] },
        accounts,
        current: spent(AGY_DEFAULT),
        view: READY,
        nowMs: resetAt * 1000,
      }),
    ).toBeNull();
  });

  it("offers nothing without a sibling to move to", () => {
    expect(pick({ accounts: [spent(AGY_DEFAULT)], current: spent(AGY_DEFAULT) })).toBeNull();
    // The other CLIs are not candidates: they are not accounts of this group.
    expect(
      pick({
        accounts: [spent(AGY_DEFAULT), DSH_DEFAULT, DSH_ACCOUNT2],
        current: spent(AGY_DEFAULT),
      }),
    ).toBeNull();
  });

  it("refuses a sibling that is signed out or spent as well", () => {
    // Moving to another dead end is not a switch, it is the same problem one
    // account further along.
    expect(
      pick({
        accounts: [spent(AGY_DEFAULT), signedOut(AGY_ACCOUNT2)],
        current: spent(AGY_DEFAULT),
      }),
    ).toBeNull();
    expect(
      pick({
        accounts: [spent(AGY_DEFAULT), spent(AGY_ACCOUNT2)],
        current: spent(AGY_DEFAULT),
      }),
    ).toBeNull();
    // Control: a signed-in sibling with quota left is the one that qualifies.
    expect(
      pick({
        accounts: [spent(AGY_DEFAULT), signedOut(AGY_ACCOUNT2), account({ ...AGY_ACCOUNT2, account: "account3", home: `${RUNTIME_HOME}/.gemini-account3` })],
        current: spent(AGY_DEFAULT),
      })?.account,
    ).toBe("account3");
  });

  it("never flags a group whose lever cannot be written", () => {
    // codex / cursor report an empty lever: the daemon gives the client no way
    // to point them at another directory, so there is no one-click switch.
    expect(
      pick({
        accounts: [spent(CODEX_DEFAULT), CODEX_ACCOUNT2, spent(CURSOR_DEFAULT), CURSOR_ACCOUNT2],
        current: spent(CODEX_DEFAULT),
      }),
    ).toBeNull();
    expect(
      pick({
        accounts: [spent(CURSOR_DEFAULT), CURSOR_ACCOUNT2],
        current: spent(CURSOR_DEFAULT),
      }),
    ).toBeNull();
  });

  it("refuses a sibling whose lever shape this client cannot write", () => {
    expect(
      pick({
        accounts: [
          spent(AGY_DEFAULT),
          account({
            cli: "agy",
            account: "account2",
            home: `${RUNTIME_HOME}/.gemini-account2`,
            lever: "custom_args:--gemini_profile",
          }),
        ],
        current: spent(AGY_DEFAULT),
      }),
    ).toBeNull();
  });

  it("refuses a sibling whose home cannot be bound", () => {
    // A relative directory has no absolute path to write, so the button would
    // be dead on click — the same rule `planAccountSwitch` enforces.
    expect(
      pick({
        accounts: [
          spent(AGY_DEFAULT),
          account({
            cli: "agy",
            account: "account2",
            home: ".gemini-account2",
            lever: "custom_args:--gemini_dir",
          }),
        ],
        current: spent(AGY_DEFAULT),
      }),
    ).toBeNull();
  });

  it("never offers a write from an untrusted list", () => {
    const accounts = [spent(AGY_DEFAULT), AGY_ACCOUNT2];
    const current = spent(AGY_DEFAULT);

    for (const view of [
      { kind: "loading" },
      { kind: "empty" },
      { kind: "error", message: "EACCES: permission denied" },
    ] satisfies AccountsViewState[]) {
      expect(pick({ accounts, current, view })).toBeNull();
    }

    // Control: the same list does offer it once the view is trustworthy.
    expect(pick({ accounts, current, view: READY })).toEqual(AGY_ACCOUNT2);
  });

  it("offers nothing when no account is in effect", () => {
    expect(pick({ accounts: [spent(AGY_DEFAULT), AGY_ACCOUNT2], current: null })).toBeNull();
  });

  it("picks a candidate that is not the account in effect even when it is reported twice", () => {
    const candidate = pick({
      accounts: [spent(AGY_DEFAULT), AGY_ACCOUNT2],
      current: spent(AGY_DEFAULT),
    });

    expect(candidate?.account).not.toBe("default");
  });
});
