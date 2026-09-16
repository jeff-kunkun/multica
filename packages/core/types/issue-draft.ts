/**
 * Requirement alignment: the conversation that happens BEFORE an issue exists.
 *
 * Creating an issue enqueues agent work, so a misunderstood request starts
 * executing before anyone has agreed what it means. A draft is that agreement
 * in progress — a hidden conversation plus a server-owned structured object —
 * and nothing is created until `finalize`.
 */

/** Where an alignment conversation has got to. */
export type IssueDraftStatus = "draft" | "ready" | "completed" | "abandoned";

/**
 * The structured issue a draft has arrived at. Only these fields are read by
 * the server at finalize; anything else the client stores alongside them is
 * preserved untouched.
 */
export interface IssueDraftPayload {
  title: string;
  description: string;
  status: string;
  priority: string;
  assignee_type?: string | null;
  assignee_id?: string | null;
  project_id?: string | null;
  parent_issue_id?: string | null;
}

/** One alignment draft as the server owns it. */
export interface IssueDraft {
  /** The alignment conversation. It is also the draft's identity — one
   *  conversation has exactly one draft. */
  chat_session_id: string;
  workspace_id: string;
  status: IssueDraftStatus;
  /** Optimistic-concurrency token. Every save bumps it, and both save and
   *  finalize refuse a token that is not the one the user was looking at. */
  revision: number;
  draft: IssueDraftPayload;
  /** The issue this draft became. Present only once status is `completed`,
   *  and stable across repeated confirms. */
  issue_id?: string | null;
  created_at: string;
  updated_at: string;
}

/** A newly opened alignment conversation and its empty draft. */
export interface IssueDraftSession {
  session_id: string;
  /** The hidden carrier that executes this conversation. */
  agent_id: string;
  /** Where this conversation actually runs. Seed the runtime picker from it so
   *  it can never disagree with what answers the next message. */
  runtime_id: string;
  draft: IssueDraft;
}

/** One unfinished alignment conversation, as listed for resuming. */
export interface IssueDraftSummary extends IssueDraft {
  title: string;
  runtime_id: string;
  last_message_content: string;
  last_message_role: string;
  last_message_at: string;
}

/** Result of confirming a draft. `issue_id` is the same value for every repeat
 *  of the same confirm — the protocol creates at most one issue per draft. */
export interface IssueDraftFinalizeResult {
  draft: IssueDraft;
  issue_id: string;
}

/** Result of rebinding a live alignment conversation to another runtime. */
export interface IssueDraftRuntimeSwitch {
  runtime_id: string;
}
