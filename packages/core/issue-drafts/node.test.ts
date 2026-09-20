// @vitest-environment node
import { describe, expect, it } from "vitest";
import { issueDraftBuiltNodeKeys, issueDraftNodeId } from "./node";
import type { Issue, IssueDraftChild } from "../types";

/**
 * Alignment NODE identity, as the server derives it.
 *
 * The vectors below come from the Go implementation
 * (`server/internal/handler/issue_draft_group.go`, `issueDraftNodeID`) run over
 * the same inputs. They are the contract this file exists to pin: the client
 * repeats the derivation only to recognise nodes the server already created,
 * and a drift between the two would silently mislabel every existing sub-issue
 * as new — the confirm would then promise creates it never performs.
 */
describe("issueDraftNodeId", () => {
  const SESSION = "0193a5f0-1c2d-7e3f-8a4b-5c6d7e8f9a0b";
  const OTHER_SESSION = "0193a5f0-0000-7000-8000-000000000000";

  it("makes the root node the conversation itself", () => {
    expect(issueDraftNodeId(SESSION, "")).toBe(SESSION);
  });

  it("derives the ids the server derives", () => {
    expect(issueDraftNodeId(SESSION, "c1")).toBe(
      "3d56681b-224b-5d51-abc1-cc1f5016091e",
    );
    expect(issueDraftNodeId(SESSION, "c2")).toBe(
      "da1cc3d0-bd89-526b-be73-5747c5331791",
    );
    expect(issueDraftNodeId(SESSION, "backend-api")).toBe(
      "d003f223-0f3a-5582-8368-c4748d5854d9",
    );
    expect(issueDraftNodeId(OTHER_SESSION, "c1")).toBe(
      "ab2cae98-2cda-50bb-a83f-d816a9569063",
    );
  });

  it("trims the key, exactly as the server trims it before hashing", () => {
    // Validation, uniqueness and derivation all have to agree on what a key IS,
    // so " c1" and "c1" must not be two different nodes here either.
    expect(issueDraftNodeId(SESSION, " c1")).toBe(
      issueDraftNodeId(SESSION, "c1"),
    );
    expect(issueDraftNodeId(SESSION, "c1 ")).toBe(
      issueDraftNodeId(SESSION, "c1"),
    );
  });

  it("gives up rather than guessing on a session that is not a UUID", () => {
    // The draft route's id is a UUID; anything else is a link that cannot
    // resolve, and hashing it would produce a plausible-looking node id for a
    // conversation that does not exist.
    expect(issueDraftNodeId("sess-42", "c1")).toBeNull();
    expect(issueDraftNodeId("", "c1")).toBeNull();
  });
});

describe("issueDraftBuiltNodeKeys", () => {
  const SESSION = "0193a5f0-1c2d-7e3f-8a4b-5c6d7e8f9a0b";

  function child(key: string, overrides: Partial<IssueDraftChild> = {}): IssueDraftChild {
    return {
      key,
      title: `Sub-issue ${key}`,
      description: "",
      status: "",
      priority: "",
      assignee_type: null,
      assignee_id: null,
      stage: 1,
      assignee_hint: null,
      ...overrides,
    };
  }

  function issue(id: string, originId?: string): Issue {
    return {
      id,
      identifier: id,
      title: id,
      origin_type: originId ? "issue_draft" : undefined,
      origin_id: originId,
    } as unknown as Issue;
  }

  it("matches a payload node to the issue its node id owns", () => {
    const built = issueDraftBuiltNodeKeys(
      [child("c1"), child("c2")],
      [
        issue("issue-a", "3d56681b-224b-5d51-abc1-cc1f5016091e"),
        issue("issue-b"),
      ],
      SESSION,
    );
    expect([...built]).toEqual(["c1"]);
  });

  it("finds nothing when there is no group yet", () => {
    expect(issueDraftBuiltNodeKeys([child("c1")], [], SESSION).size).toBe(0);
  });

  it("does not match on a title someone has edited", () => {
    // The whole reason the join is by node id: the panel lets the user rename a
    // row, and the server never rewrites an adopted issue's title. Matching on
    // the title would call the renamed row new and promise a create the server
    // skips.
    const built = issueDraftBuiltNodeKeys(
      [child("c1", { title: "Renamed in this round" })],
      [issue("issue-a", "3d56681b-224b-5d51-abc1-cc1f5016091e")],
      SESSION,
    );
    expect([...built]).toEqual(["c1"]);
  });

  it("ignores a node whose session cannot be derived", () => {
    const built = issueDraftBuiltNodeKeys(
      [child("c1")],
      [issue("issue-a", SESSION)],
      "not-a-uuid",
    );
    expect(built.size).toBe(0);
  });
});
