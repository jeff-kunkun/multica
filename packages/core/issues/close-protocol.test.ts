// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { IssueMetadata } from "../types";
import {
  CLOSE_PROTOCOL_KEYS,
  closeProtocolIsStuck,
  closeProtocolNextOwnerId,
  closeProtocolWaitingOn,
  readCloseProtocol,
} from "./close-protocol";

// Captured from `multica issue children` on DENE-229 (2026-09-15). Stage 5
// renders this family; the helper must match the live metadata bags.
const DENE_230_METADATA: IssueMetadata = {};

const DENE_231_METADATA: IssueMetadata = {
  "close.at": "2026-09-15T11:52:15Z",
  "close.conclusion": "delivered",
  "close.evidence_comment_id": "01a0a4e9-1c4e-75a6-8895-79f1210f494e",
  "close.next_owner_id": "",
  "close.next_owner_type": "none",
  "close.status": "done",
  "close.waiting_on": "",
  "close.wake_action": "stage_done",
};

const DENE_232_METADATA: IssueMetadata = {
  "close.at": "2026-09-15T12:10:31Z",
  "close.conclusion": "delivered",
  "close.evidence_comment_id": "01a0a4f9-d366-7e75-a6ab-60a6f821377d",
  "close.next_owner_id": "",
  "close.next_owner_type": "none",
  "close.status": "done",
  "close.waiting_on": "",
  "close.wake_action": "stage_done",
};

const DENE_233_METADATA: IssueMetadata = {
  "close.at": "2026-09-15T13:04:42Z",
  "close.conclusion": "delivered",
  "close.evidence_comment_id": "01a0a52b-6f4e-7385-9c78-7e0e140e05fc",
  "close.next_owner_id": "",
  "close.next_owner_type": "none",
  "close.status": "done",
  "close.waiting_on": "",
  "close.wake_action": "stage_done",
};

describe("readCloseProtocol", () => {
  it("treats an empty bag as not closed under the protocol (DENE-230)", () => {
    const view = readCloseProtocol(DENE_230_METADATA, "done");
    expect(view.complete).toBe(false);
    expect(view.missingKeys).toEqual([...CLOSE_PROTOCOL_KEYS]);
    expect(view.conclusion).toBeNull();
    expect(view.statusDrift).toBe(false);
  });

  it("reads a delivered Stage 2 close that matches issue.status (DENE-231)", () => {
    const view = readCloseProtocol(DENE_231_METADATA, "done");
    expect(view.complete).toBe(true);
    expect(view.missingKeys).toEqual([]);
    expect(view.conclusion).toBe("delivered");
    expect(view.closeStatus).toBe("done");
    expect(view.statusDrift).toBe(false);
    expect(view.nextOwnerType).toBe("none");
    expect(view.nextOwnerId).toBe("");
    expect(view.wakeAction).toBe("stage_done");
    expect(view.waitingOn).toBe("");
    expect(view.at).toBe("2026-09-15T11:52:15Z");
  });

  it("reads DENE-232 and DENE-233 as complete delivered closes", () => {
    for (const meta of [DENE_232_METADATA, DENE_233_METADATA]) {
      const view = readCloseProtocol(meta, "done");
      expect(view.complete).toBe(true);
      expect(view.statusDrift).toBe(false);
      expect(view.conclusion).toBe("delivered");
      expect(view.closeStatus).toBe("done");
    }
  });

  it("flags close.status drift that happened on DENE-232/233 before the bag was repaired", () => {
    // Historical: close.status=in_review while the issue was already done.
    const drifted: IssueMetadata = {
      ...DENE_233_METADATA,
      "close.status": "in_review",
      "close.conclusion": "awaiting_review",
    };
    const view = readCloseProtocol(drifted, "done");
    expect(view.complete).toBe(true);
    expect(view.statusDrift).toBe(true);
    expect(view.closeStatus).toBe("in_review");
  });

  it("flags drift even when other close.* keys are still missing", () => {
    const view = readCloseProtocol(
      { "close.status": "in_review" },
      "done",
    );
    expect(view.complete).toBe(false);
    expect(view.statusDrift).toBe(true);
    expect(view.missingKeys).toContain("close.conclusion");
  });

  it("keeps empty-string waiting_on and next_owner_id as present keys", () => {
    const view = readCloseProtocol(DENE_231_METADATA, "done");
    expect(view.complete).toBe(true);
    expect(closeProtocolWaitingOn(view.waitingOn)).toBeNull();
    expect(closeProtocolNextOwnerId(view.nextOwnerType, view.nextOwnerId)).toBeNull();
  });

  it("surfaces a cross-ticket wait and a named next owner", () => {
    const view = readCloseProtocol(
      {
        "close.conclusion": "awaiting_review",
        "close.status": "in_review",
        "close.evidence_comment_id": "comment-1",
        "close.next_owner_type": "agent",
        "close.next_owner_id": "1cbd7845-acbd-47d7-b0ea-582ec3d9f01f",
        "close.wake_action": "mention",
        "close.waiting_on": "DENE-196",
        "close.at": "2026-09-15T10:00:00Z",
      },
      "in_review",
    );
    expect(view.complete).toBe(true);
    expect(view.statusDrift).toBe(false);
    expect(closeProtocolWaitingOn(view.waitingOn)).toBe("DENE-196");
    expect(closeProtocolNextOwnerId(view.nextOwnerType, view.nextOwnerId)).toBe(
      "1cbd7845-acbd-47d7-b0ea-582ec3d9f01f",
    );
  });

  it("treats a missing metadata object like an empty bag", () => {
    const view = readCloseProtocol(undefined, "in_progress");
    expect(view.complete).toBe(false);
    expect(view.missingKeys).toHaveLength(8);
  });
});

describe("closeProtocolIsStuck", () => {
  it("treats blocked and in_review issue status as stuck", () => {
    expect(closeProtocolIsStuck("blocked", "delivered")).toBe(true);
    expect(closeProtocolIsStuck("in_review", null)).toBe(true);
    expect(closeProtocolIsStuck("done", "delivered")).toBe(false);
  });

  it("treats awaiting_* / blocked conclusions as stuck even if status drifted", () => {
    expect(closeProtocolIsStuck("done", "awaiting_review")).toBe(true);
    expect(closeProtocolIsStuck("done", "awaiting_human")).toBe(true);
    expect(closeProtocolIsStuck("todo", "blocked")).toBe(true);
  });
});
