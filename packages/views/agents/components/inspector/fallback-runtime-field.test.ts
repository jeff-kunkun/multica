// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "@multica/core/types";
import { fallbackRuntimeCandidates } from "./fallback-runtime-field";

const ME = "user-me";
const OTHER = "user-other";

function rt(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Claude (host.local)",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local",
    metadata: {},
    owner_id: ME,
    visibility: "private",
    last_seen_at: "2026-10-11T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides,
  } as AgentRuntime;
}

const PRIMARY = rt({ id: "primary" });
const SAME_OWNER_SAME_PROVIDER = rt({ id: "twin", name: "Claude (desk.local)" });
const SAME_OWNER_OTHER_PROVIDER = rt({ id: "codex", provider: "codex" });
const OTHER_OWNER_PRIVATE = rt({ id: "theirs", owner_id: OTHER });
const OTHER_OWNER_SHARED = rt({
  id: "theirs-shared",
  owner_id: OTHER,
  visibility: "public",
});

describe("fallbackRuntimeCandidates", () => {
  it("keeps only a different runtime with the primary's owner and provider", () => {
    const got = fallbackRuntimeCandidates(
      [
        PRIMARY,
        SAME_OWNER_SAME_PROVIDER,
        SAME_OWNER_OTHER_PROVIDER,
        OTHER_OWNER_PRIVATE,
        OTHER_OWNER_SHARED,
      ],
      "primary",
      ME,
    );
    expect(got.map((r) => r.id)).toEqual(["twin"]);
  });

  it("offers nothing without a primary runtime", () => {
    expect(fallbackRuntimeCandidates([PRIMARY], "", ME)).toEqual([]);
    expect(fallbackRuntimeCandidates([PRIMARY], "missing", ME)).toEqual([]);
  });

  it("drops a same-owner runtime this user cannot select", () => {
    // Someone else's private pair: compatible with each other, but not
    // selectable by me, so the picker must not offer it.
    const theirPrimary = rt({ id: "p2", owner_id: OTHER });
    const theirTwin = rt({ id: "t2", owner_id: OTHER });
    expect(
      fallbackRuntimeCandidates([theirPrimary, theirTwin], "p2", ME),
    ).toEqual([]);
  });
});
