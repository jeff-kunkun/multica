import { describe, expect, it } from "vitest";
import {
  AppConfigSchema,
  WorkspaceBotListSchema,
  WorkspaceBotWithSecretsSchema,
  AgentTaskListSchema,
  AgentActivityBucketListSchema,
  TaskMessageListSchema,
  CommentTriggerPreviewSchema,
  DashboardAgentRunTimeListSchema,
  DashboardRunTimeDailyListSchema,
  DashboardFailureByAgentListSchema,
  DashboardFailureDailyListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  ChatDraftRestoresResponseSchema,
  ChatPendingTaskSchema,
  ChatSessionListSchema,
  ChatSessionSchema,
  PrioritizeQueuedChatTaskResponseSchema,
  CreateFeedbackResponseSchema,
  EMPTY_CHAT_DRAFT_RESTORES,
  EMPTY_CHAT_PENDING_TASK,
  EMPTY_CHAT_SESSION,
  EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
  EMPTY_CREATE_FEEDBACK_RESPONSE,
  EMPTY_INBOX_ITEMS,
  EMPTY_INBOX_UNREAD_SUMMARY,
  EMPTY_SEARCH_PROJECTS_RESPONSE,
  EMPTY_USER,
  InboxItemListSchema,
  InboxUnreadSummarySchema,
  IssueTriggerPreviewSchema,
  ListIssuesResponseSchema,
  ListPropertiesResponseSchema,
  MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
  RuntimeModelListRequestSchema,
  SearchProjectsResponseSchema,
  RuntimeHourlyActivityListSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByHourListSchema,
  RuntimeUsageListSchema,
  SendChatMessageResponseSchema,
  SourceContextPreviewSchema,
  TimelineEntriesSchema,
  UserSchema,
} from "./schemas";
import { IssueViewSchema, IssueViewListSchema } from "./schemas";
import {
  ListIssueStatusesResponseSchema,
  IssueStatusEntrySchema,
  EMPTY_LIST_ISSUE_STATUSES_RESPONSE,
  EMPTY_ISSUE_STATUS_ENTRY,
} from "./schemas";
import {
  EMPTY_ISSUE_PLANS,
  EMPTY_MEETINGS,
  IssuePlansSchema,
  MeetingListSchema,
  PlanApproverListSchema,
} from "./schemas";
import { parseWithFallback } from "./schema";

const baseIssue = {
  id: "11111111-1111-1111-1111-111111111111",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Test",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  stage: null,
  start_date: null,
  due_date: null,
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("ChatSessionSchema", () => {
  const baseSession = {
    id: "chat-1",
    workspace_id: "ws-1",
    agent_id: "agent-1",
    creator_id: "user-1",
    title: "Chat",
    status: "active",
    has_unread: false,
    created_at: "2026-08-18T00:00:00Z",
    updated_at: "2026-08-18T00:00:00Z",
  };

  it("degrades malformed session metadata without dropping the Chat", () => {
    const parsed = ChatSessionSchema.parse({
      ...baseSession,
      last_message: "not-an-object",
    });
    expect(parsed.last_message).toBeUndefined();
    expect(parsed.id).toBe("chat-1");

    expect(parseWithFallback({ id: 42 }, ChatSessionSchema, EMPTY_CHAT_SESSION, {
      endpoint: "GET /api/chat/sessions/:id",
    })).toEqual(EMPTY_CHAT_SESSION);
  });

  it("drops one malformed list item without hiding the other Chats", () => {
    const parsed = ChatSessionListSchema.parse([
      baseSession,
      { ...baseSession, id: 42 },
      {
        ...baseSession,
        id: "no-response-chat",
        last_message: {
          content: "Welcome",
          role: "assistant",
          created_at: "2026-08-18T00:00:00Z",
          message_kind: "no_response",
        },
      },
    ]);

    expect(parsed.map((session) => session.id)).toEqual(["chat-1", "no-response-chat"]);
    expect(parsed[1]?.last_message?.message_kind).toBe("no_response");
  });
});
describe("IssueSchema (via ListIssuesResponseSchema)", () => {
  // A custom status key can be derived rather than readable — "客户确认" becomes
  // `in_review_2` — so the display name travels with it. The field has to
  // survive a server that predates it, since an issue that fails validation
  // degrades to a stub rather than losing one field. (MUL-6749)
  it("carries a custom status's display name", () => {
    const parsed = ListIssuesResponseSchema.parse({
      issues: [{ ...baseIssue, status: "in_review_2", status_name: "客户确认" }],
      total: 1,
    });
    expect(parsed.issues[0]?.status_name).toBe("客户确认");
  });
  it("drops only a malformed status_name, keeping the issue and the list", () => {
    for (const bad of [42, { name: "x" }, ["x"], true]) {
      const parsed = ListIssuesResponseSchema.parse({
        issues: [{ ...baseIssue, status: "in_review_2", status_name: bad }],
        total: 1,
      });
      expect(parsed.issues).toHaveLength(1);
      expect(parsed.issues[0]?.id).toBe(baseIssue.id);
      expect(parsed.issues[0]?.status).toBe("in_review_2");
      expect(parsed.issues[0]?.status_name).toBeUndefined();
    }
  });
  it("still parses an issue from a server that does not send status_name", () => {
    const { status_name: _omitted, ...withoutName } = { ...baseIssue, status_name: "x" };
    const parsed = ListIssuesResponseSchema.parse({ issues: [withoutName], total: 1 });
    expect(parsed.issues[0]?.id).toBe(baseIssue.id);
    expect(parsed.issues[0]?.status_name).toBeUndefined();
  });
  it("keeps the issue while independently dropping a malformed source context", () => {
    const parsed = ListIssuesResponseSchema.parse({
      issues: [{ ...baseIssue, source_context: { snapshot: "bad" } }],
      total: 1,
    });
    expect(parsed.issues[0]?.id).toBe(baseIssue.id);
    expect(parsed.issues[0]?.source_context).toBeUndefined();
  });
  it("parses source-context change reasons without requiring them from older servers", () => {
    const sourceContext = {
      id: "context-1",
      version: 1,
      usage: "read_only_historical_background",
      captured_at: "2026-08-21T12:00:00Z",
      display_state: "changed",
      source_issue_state: "changed",
      comment_thread_state: "unchanged",
      anchor_comment_state: "available",
      can_open_current_source: true,
      change_reasons: ["issue_description_attachments"],
      change_details: {
        changed_comment_ids: ["comment-1"],
        added_comments: [{
          id: "comment-2", parent_id: "comment-1", type: "comment", content: "new reply",
          author: { type: "member", id: "user-2", name: "Bob" },
          created_at: "later", updated_at: "later", revision: 1, attachments: [],
        }],
        removed_comment_ids: ["comment-3"],
        description_attachment_changes: [{
          kind: "removed", attachment_id: "attachment-1", filename: "old.txt",
        }],
      },
      snapshot: {
        source_issue: {
          id: "issue-1", identifier: "MUL-1", number: 1, title: "Source",
          description: null, created_at: "now", updated_at: "now", revision: 1,
          attachments: [],
        },
        comment_thread: [{
          id: "comment-1", parent_id: null, type: "comment", content: "history",
          author: { type: "member", id: "user-1", name: "Alice" },
          created_at: "now", updated_at: "now", revision: 1, attachments: [],
        }],
        anchor_comment_id: "comment-1",
      },
    };
    const parsed = ListIssuesResponseSchema.parse({
      issues: [{ ...baseIssue, source_context: sourceContext }],
      total: 1,
    });
    expect(parsed.issues[0]?.source_context?.change_reasons).toEqual(["issue_description_attachments"]);
    expect(parsed.issues[0]?.source_context?.change_details).toEqual(sourceContext.change_details);

    const { change_reasons: _reasonsOmitted, change_details: _detailsOmitted, ...legacyContext } = sourceContext;
    const legacy = ListIssuesResponseSchema.parse({
      issues: [{ ...baseIssue, source_context: legacyContext }],
      total: 1,
    });
    expect(legacy.issues[0]?.source_context?.change_reasons).toBeUndefined();
    expect(legacy.issues[0]?.source_context?.change_details).toBeUndefined();

    const malformed = ListIssuesResponseSchema.parse({
      issues: [{
        ...baseIssue,
        source_context: {
          ...sourceContext,
          change_details: { ...sourceContext.change_details, added_comments: [{ id: 42 }] },
        },
      }],
      total: 1,
    });
    expect(malformed.issues[0]?.source_context).toBeUndefined();
  });
  it("accepts null activity during backfill and rejects malformed activity", () => {
    const parsed = ListIssuesResponseSchema.parse({
      issues: [{ ...baseIssue, last_activity_at: null }],
      total: 1,
    });
    expect(parsed.issues[0]?.last_activity_at).toBeNull();
    expect(() =>
      ListIssuesResponseSchema.parse({
        issues: [{ ...baseIssue, last_activity_at: 42 }],
        total: 1,
      }),
    ).toThrow();
  });

  it("accepts a primitive metadata KV map", () => {
    const payload = {
      issues: [
        {
          ...baseIssue,
          metadata: { pipeline_status: "waiting", pr_number: 3, is_blocked: true },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({
      pipeline_status: "waiting",
      pr_number: 3,
      is_blocked: true,
    });
  });

  it("defaults metadata to {} when the server omits it (older backend)", () => {
    const { metadata: _omit, ...issueWithoutMetadata } = baseIssue;
    const payload = { issues: [issueWithoutMetadata], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({});
  });

  it("rejects metadata with non-primitive values (nested object)", () => {
    const payload = {
      issues: [{ ...baseIssue, metadata: { nested: { x: 1 } } }],
      total: 1,
    };
    expect(ListIssuesResponseSchema.safeParse(payload).success).toBe(false);
  });

  it("accepts a numeric stage", () => {
    const payload = { issues: [{ ...baseIssue, stage: 2 }], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.stage).toBe(2);
  });

  it("defaults stage to null when the server omits it (older backend)", () => {
    const { stage: _omit, ...issueWithoutStage } = baseIssue;
    const payload = { issues: [issueWithoutStage], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.stage).toBeNull();
  });

  it("accepts custom property values including multi_select arrays", () => {
    const payload = {
      issues: [
        {
          ...baseIssue,
          properties: { "def-1": "opt-a", "def-2": ["opt-x", "opt-y"], "def-3": 3.5, "def-4": true },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.properties).toEqual({
      "def-1": "opt-a",
      "def-2": ["opt-x", "opt-y"],
      "def-3": 3.5,
      "def-4": true,
    });
  });

  it("defaults properties to {} when the server omits it (older backend)", () => {
    const parsed = ListIssuesResponseSchema.parse({ issues: [baseIssue], total: 1 });
    expect(parsed.issues[0]?.properties).toEqual({});
  });

  it("drops unknown-shaped property values instead of failing the issue parse", () => {
    // Forward compat: a future server type (actor/relation) may ship object
    // values. That one entry must disappear; the issue and its other
    // properties must survive — a full parse failure would blank the whole
    // list through parseWithFallback on installed desktop builds.
    const payload = {
      issues: [
        {
          ...baseIssue,
          properties: { "def-1": { nested: 1 }, "def-2": "opt-a" },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.properties).toEqual({ "def-2": "opt-a" });
  });
});

describe("SourceContextPreviewSchema", () => {
  it("parses a thread-history preview and rejects a missing token", () => {
    const preview = {
      source_issue: {
        id: "issue-1", identifier: "MUL-1", number: 1, title: "Source",
        description: null, created_at: "now", updated_at: "now", revision: 1,
        attachments: [],
      },
      comment_thread: [{
        id: "comment-1", parent_id: null, type: "comment", content: "history",
        author: { type: "member", id: "user-1", name: "Alice" },
        created_at: "now", updated_at: "now", revision: 1, attachments: [],
      }],
      anchor_comment_id: "comment-1",
      capture_token: "sha256:token",
      limits: { comment_count: 1, text_bytes: 7, attachment_count: 0, attachment_bytes: 0 },
    };
    expect(SourceContextPreviewSchema.parse(preview).comment_thread).toHaveLength(1);
    expect(() => SourceContextPreviewSchema.parse({ ...preview, capture_token: "" })).toThrow();
  });

  it("normalizes null attachment lists from early source-context servers", () => {
    const preview = {
      source_issue: {
        id: "issue-1", identifier: "MUL-1", number: 1, title: "Source",
        description: null, created_at: "now", updated_at: "now", revision: 1,
        attachments: null,
      },
      comment_thread: [{
        id: "comment-1", parent_id: null, type: "comment", content: "history",
        author: { type: "member", id: "user-1", name: "Alice" },
        created_at: "now", updated_at: "now", revision: 1, attachments: null,
      }],
      anchor_comment_id: "comment-1",
      capture_token: "sha256:token",
      limits: { comment_count: 1, text_bytes: 7, attachment_count: 0, attachment_bytes: 0 },
    };

    const parsed = SourceContextPreviewSchema.parse(preview);
    expect(parsed.source_issue.attachments).toEqual([]);
    expect(parsed.comment_thread[0]?.attachments).toEqual([]);
  });
});

describe("IssuePropertySchema (via ListPropertiesResponseSchema)", () => {
  const baseProperty = {
    id: "22222222-2222-2222-2222-222222222222",
    workspace_id: "ws-1",
    name: "Severity",
    type: "select",
    description: "",
    icon: "flag",
    config: { options: [{ id: "opt-1", name: "Critical", color: "#ef4444" }] },
    position: 1,
    archived: false,
    archived_at: null,
    usage_count: 2,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("parses a full definition", () => {
    const parsed = ListPropertiesResponseSchema.parse({ properties: [baseProperty], total: 1 });
    expect(parsed.properties[0]?.config.options?.[0]?.name).toBe("Critical");
    expect(parsed.properties[0]?.icon).toBe("flag");
  });

  it("survives a malformed response by defaulting the list", () => {
    const parsed = ListPropertiesResponseSchema.parse({});
    expect(parsed.properties).toEqual([]);
    expect(parsed.total).toBe(0);
  });

  it("keeps unknown property types as strings (forward compat)", () => {
    const parsed = ListPropertiesResponseSchema.parse({
      properties: [{ ...baseProperty, type: "relation", config: {} }],
      total: 1,
    });
    expect(parsed.properties[0]?.type).toBe("relation");
  });

  it("defaults config when the server sends none", () => {
    const { config: _omit, ...withoutConfig } = baseProperty;
    const parsed = ListPropertiesResponseSchema.parse({ properties: [withoutConfig], total: 1 });
    expect(parsed.properties[0]?.config).toEqual({});
  });

  it("defaults icon for an older server response", () => {
    const { icon: _omit, ...withoutIcon } = baseProperty;
    const parsed = ListPropertiesResponseSchema.parse({ properties: [withoutIcon], total: 1 });
    expect(parsed.properties[0]?.icon).toBe("");
  });
});

// POST /api/issues/preview-trigger feeds this schema through parseWithFallback
// in client.previewIssueTrigger with fallback { triggers: [], total_count: 0 }
// (MUL-3375). The four entry points read it to decide "will this start a run",
// so malformed / missing / null drift must degrade to "nothing will start"
// rather than throw into the picker/modal.
const PREVIEW_FALLBACK = { triggers: [], total_count: 0 };
const PREVIEW_ENDPOINT = { endpoint: "POST /api/issues/preview-trigger" };

describe("IssueTriggerPreviewSchema", () => {
  it("parses a well-formed response", () => {
    const parsed = IssueTriggerPreviewSchema.parse({
      triggers: [
        { issue_id: "i1", agent_id: "a1", source: "assign" },
        { issue_id: "i2", agent_id: "a2", source: "status" },
      ],
      total_count: 2,
    });
    expect(parsed.total_count).toBe(2);
    expect(parsed.triggers).toHaveLength(2);
    expect(parsed.triggers[0]).toMatchObject({ issue_id: "i1", agent_id: "a1", source: "assign" });
  });

  it("defaults missing top-level fields (empty / older backend)", () => {
    const parsed = IssueTriggerPreviewSchema.parse({});
    expect(parsed.triggers).toEqual([]);
    expect(parsed.total_count).toBe(0);
  });

  it("defaults missing optional item fields, keeping required issue_id", () => {
    const parsed = IssueTriggerPreviewSchema.parse({ triggers: [{ issue_id: "i1" }], total_count: 1 });
    expect(parsed.triggers[0]).toEqual({
      issue_id: "i1",
      agent_id: "",
      source: "",
    });
  });

  it("parseWithFallback returns the fallback for a malformed shape (triggers not an array)", () => {
    const parsed = parseWithFallback(
      { triggers: "nope", total_count: 1 },
      IssueTriggerPreviewSchema,
      PREVIEW_FALLBACK,
      PREVIEW_ENDPOINT,
    );
    expect(parsed).toEqual(PREVIEW_FALLBACK);
  });

  it("parseWithFallback returns the fallback when an item drops the required issue_id", () => {
    const parsed = parseWithFallback(
      { triggers: [{ agent_id: "a1", source: "assign" }], total_count: 1 },
      IssueTriggerPreviewSchema,
      PREVIEW_FALLBACK,
      PREVIEW_ENDPOINT,
    );
    expect(parsed).toEqual(PREVIEW_FALLBACK);
  });

  it("parseWithFallback returns the fallback for a wrong-typed total_count", () => {
    const parsed = parseWithFallback(
      { triggers: [], total_count: "5" },
      IssueTriggerPreviewSchema,
      PREVIEW_FALLBACK,
      PREVIEW_ENDPOINT,
    );
    expect(parsed).toEqual(PREVIEW_FALLBACK);
  });

  it("parseWithFallback returns the fallback for null / non-object bodies", () => {
    expect(parseWithFallback(null, IssueTriggerPreviewSchema, PREVIEW_FALLBACK, PREVIEW_ENDPOINT)).toEqual(PREVIEW_FALLBACK);
    expect(parseWithFallback("oops", IssueTriggerPreviewSchema, PREVIEW_FALLBACK, PREVIEW_ENDPOINT)).toEqual(PREVIEW_FALLBACK);
  });
});

describe("TimelineEntriesSchema", () => {
  it("preserves run-bound supplement delivery receipts", () => {
    const parsed = TimelineEntriesSchema.parse([{
      type: "comment",
      id: "supplement-1",
      actor_type: "member",
      actor_id: "user-1",
      created_at: "2026-01-01T00:00:00Z",
      content: "also cover rollback",
      supplement_task_id: "task-1",
      supplement_status: "delivered",
      supplement_delivered_at: "2026-01-01T00:00:01Z",
    }]);
    expect(parsed[0]).toMatchObject({
      supplement_task_id: "task-1",
      supplement_status: "delivered",
      supplement_delivered_at: "2026-01-01T00:00:01Z",
    });
  });

  it("preserves source_task_id for agent failure comments", () => {
    const parsed = TimelineEntriesSchema.parse([
      {
        type: "comment",
        id: "comment-1",
        actor_type: "agent",
        actor_id: "agent-1",
        created_at: "2026-01-01T00:00:00Z",
        content: "API Error: 500 Internal server error",
        comment_type: "system",
        source_task_id: "task-1",
      },
    ]);

    expect(parsed[0]?.source_task_id).toBe("task-1");
  });

  it("preserves server-hydrated historical actor identity", () => {
    const parsed = TimelineEntriesSchema.parse([
      {
        type: "comment",
        id: "comment-1",
        actor_type: "member",
        actor_id: "former-user-1",
        actor_name: "Former Member",
        actor_avatar_url: "https://profiles.example.com/former.png",
        created_at: "2026-01-01T00:00:00Z",
        content: "Authored before leaving",
      },
    ]);

    expect(parsed[0]?.actor_name).toBe("Former Member");
    expect(parsed[0]?.actor_avatar_url).toBe(
      "https://profiles.example.com/former.png",
    );
  });

  it("preserves the deleted-comment tombstone marker", () => {
    const parsed = TimelineEntriesSchema.parse([
      {
        type: "comment",
        id: "comment-1",
        actor_type: "member",
        actor_id: "user-1",
        created_at: "2026-01-01T00:00:00Z",
        content: "",
        deleted_at: "2026-01-02T00:00:00Z",
      },
    ]);

    expect(parsed[0]?.deleted_at).toBe("2026-01-02T00:00:00Z");
  });

  it("reads a malformed tombstone marker as a live comment instead of failing the timeline", () => {
    const parsed = TimelineEntriesSchema.parse([
      {
        type: "comment",
        id: "comment-1",
        actor_type: "member",
        actor_id: "user-1",
        created_at: "2026-01-01T00:00:00Z",
        content: "still here",
        deleted_at: 42,
      },
    ]);

    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.deleted_at).toBeUndefined();
  });
});

describe("AgentTaskListSchema", () => {
  it("preserves negotiated supplement capability, ordered coverage and permission", () => {
    const parsed = AgentTaskListSchema.parse([{
      id: "run",
      supplement_capability: "task-supplement-v1",
      supplement_comment_ids: ["comment-1", "comment-2"],
      can_supplement: true,
    }]);
    expect(parsed[0]).toMatchObject({
      supplement_capability: "task-supplement-v1",
      supplement_comment_ids: ["comment-1", "comment-2"],
      can_supplement: true,
    });
  });

  it.each([true, false, undefined, null, "true", 1])("safely parses comment cancellation metadata: %s", (value) => {
    const parsed = AgentTaskListSchema.parse([{ id: "run", cancelled_by_comment_change: value }]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.cancelled_by_comment_change).toBe(typeof value === "boolean" ? value : undefined);
  });

  it("parses cancellation actor metadata without making it required", () => {
    const parsed = AgentTaskListSchema.parse([
      { id: "new", cancelled_by: { type: "member", id: "user-1", name: "Jiayuan" } },
      { id: "legacy" },
      { id: "malformed", cancelled_by: "member" },
    ]);

    expect(parsed[0]?.cancelled_by).toEqual({ type: "member", id: "user-1", name: "Jiayuan" });
    expect(parsed[1]?.cancelled_by).toBeUndefined();
    expect(parsed[2]?.cancelled_by).toBeUndefined();
  });

  const task = {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "queued",
    priority: 0,
    dispatched_at: null,
    started_at: null,
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-07-10T00:00:00Z",
    trigger_comment_id: "comment-3",
  };

  it("preserves planned and delivered comment IDs for a task run", () => {
    const parsed = AgentTaskListSchema.parse([
      {
        ...task,
        coalesced_comment_ids: ["comment-1", "comment-2"],
        delivered_comment_ids: ["comment-1", "comment-2", "comment-3"],
      },
    ]);

    expect(parsed[0]?.trigger_comment_id).toBe("comment-3");
    expect(parsed[0]?.coalesced_comment_ids).toEqual([
      "comment-1",
      "comment-2",
    ]);
    expect(parsed[0]?.delivered_comment_ids).toEqual([
      "comment-1",
      "comment-2",
      "comment-3",
    ]);
  });

  it("accepts task payloads from older backends without comment coverage", () => {
    const parsed = AgentTaskListSchema.parse([task]);
    expect(parsed[0]?.coalesced_comment_ids).toBeUndefined();
    expect(parsed[0]?.delivered_comment_ids).toBeUndefined();
  });

  it("degrades malformed optional coverage without dropping task rows", () => {
    const parsed = AgentTaskListSchema.parse([
      {
        ...task,
        coalesced_comment_ids: ["comment-1", 2],
        delivered_comment_ids: "not-an-array",
      },
      {
        ...task,
        id: "task-2",
        delivered_comment_ids: ["comment-2", "comment-3"],
      },
    ]);

    expect(parsed).toHaveLength(2);
    expect(parsed[0]?.coalesced_comment_ids).toBeUndefined();
    expect(parsed[0]?.delivered_comment_ids).toBeUndefined();
    expect(parsed[1]?.delivered_comment_ids).toEqual([
      "comment-2",
      "comment-3",
    ]);
  });

  it("accepts durable workdir metadata from newer backends", () => {
    const parsed = AgentTaskListSchema.parse([
      {
        ...task,
        status: "completed",
        work_dir: "/managed/task/worktree",
        durable_work_dir: "/Users/dev/project",
        relative_durable_work_dir: "project",
        branch_name: "agent/j/abc12345",
      },
    ]);

    expect(parsed[0]).toMatchObject({
      durable_work_dir: "/Users/dev/project",
      relative_durable_work_dir: "project",
      branch_name: "agent/j/abc12345",
    });
  });

  it("degrades malformed optional path metadata without dropping task rows", () => {
    const parsed = AgentTaskListSchema.parse([
      {
        ...task,
        work_dir: 1,
        durable_work_dir: { path: "/project" },
        relative_durable_work_dir: false,
        branch_name: ["agent/j/abc12345"],
      },
    ]);

    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.work_dir).toBeUndefined();
    expect(parsed[0]?.durable_work_dir).toBeUndefined();
    expect(parsed[0]?.relative_durable_work_dir).toBeUndefined();
    expect(parsed[0]?.branch_name).toBeUndefined();
  });
});

describe("ChatDraftRestoresResponseSchema", () => {
  it("parses a well-formed response with attachments", () => {
    const parsed = parseWithFallback(
      {
        restores: [
          {
            id: "msg-1",
            chat_session_id: "s-1",
            task_id: "t-1",
            content: "run the thing",
            attachments: [{ id: "att-1", filename: "notes.txt" }],
            created_at: "2026-07-01T00:00:00Z",
          },
        ],
      },
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      { endpoint: "test" },
    );
    expect(parsed.restores).toHaveLength(1);
    expect(parsed.restores[0]?.content).toBe("run the thing");
    expect(parsed.restores[0]?.attachments?.[0]?.id).toBe("att-1");
  });

  it("defaults a missing restores array instead of crashing the composer", () => {
    const parsed = parseWithFallback(
      {},
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      { endpoint: "test" },
    );
    expect(parsed.restores).toEqual([]);
  });

  it("falls back to the empty response on a malformed row", () => {
    // A row without the consume key (id) is unusable — the whole response
    // falls back and the durable rows simply stay pending server-side.
    const parsed = parseWithFallback(
      { restores: [{ chat_session_id: "s-1", content: 42 }] },
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      { endpoint: "test" },
    );
    expect(parsed).toEqual(EMPTY_CHAT_DRAFT_RESTORES);
  });
});

describe("ChatPendingTaskSchema", () => {
  const ENDPOINT = { endpoint: "GET /api/chat/sessions/:id/pending-task" };

  it("keeps legacy responses compatible when queued_tasks is absent", () => {
    const parsed = parseWithFallback(
      {
        task_id: "task-active",
        status: "running",
        created_at: "2026-07-01T00:00:00Z",
      },
      ChatPendingTaskSchema,
      EMPTY_CHAT_PENDING_TASK,
      ENDPOINT,
    );

    expect(parsed).toMatchObject({
      task_id: "task-active",
      status: "running",
    });
    expect(parsed.queued_tasks).toBeUndefined();
  });

  it("parses queued task summaries", () => {
    const parsed = ChatPendingTaskSchema.parse({
      task_id: "task-active",
      status: "running",
      queued_tasks: [
        {
          task_id: "task-queued",
          status: "queued",
          content: "Follow up after the current task",
          created_at: "2026-07-01T00:01:00Z",
        },
      ],
    });

    expect(parsed.queued_tasks).toEqual([
      expect.objectContaining({
        task_id: "task-queued",
        content: "Follow up after the current task",
      }),
    ]);
  });

  it("keeps a valid head and ignores only malformed queued rows", () => {
    const parsed = parseWithFallback(
      {
        task_id: "task-active",
        queued_tasks: [{ task_id: 42, status: "queued" }],
      },
      ChatPendingTaskSchema,
      EMPTY_CHAT_PENDING_TASK,
      ENDPOINT,
    );

    expect(parsed).toEqual({
      task_id: "task-active",
      queued_tasks: [],
    });
  });
});

describe("SendChatMessageResponseSchema", () => {
  const base = {
    message_id: "message-1",
    task_id: "task-1",
    created_at: "2026-08-05T00:00:00Z",
  };

  it("parses the server-authoritative queue position", () => {
    expect(SendChatMessageResponseSchema.parse({ ...base, queued: false }).queued).toBe(false);
  });

  it("ignores a malformed additive queue position without losing the accepted send", () => {
    expect(SendChatMessageResponseSchema.parse({ ...base, queued: "no" }).queued).toBeUndefined();
  });
});

describe("PrioritizeQueuedChatTaskResponseSchema", () => {
  const ENDPOINT = {
    endpoint: "POST /api/chat/sessions/:id/queued-tasks/:taskId/prioritize",
  };

  it("parses the prioritized task id", () => {
    expect(
      PrioritizeQueuedChatTaskResponseSchema.parse({
        task_id: "task-queued",
        active_task_id: "task-active",
      }),
    ).toEqual({
      task_id: "task-queued",
      active_task_id: "task-active",
    });
  });

  it("falls back when task_id is malformed", () => {
    expect(
      parseWithFallback(
        { task_id: 42 },
        PrioritizeQueuedChatTaskResponseSchema,
        EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
        ENDPOINT,
      ),
    ).toBe(EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE);
  });
});

describe("CreateFeedbackResponseSchema", () => {
  const ENDPOINT = { endpoint: "POST /api/feedback" };

  it("parses a well-formed response and preserves extra fields", () => {
    const parsed = parseWithFallback(
      { id: "feedback-1", created_at: "2026-06-26T00:00:00Z", future_field: true },
      CreateFeedbackResponseSchema,
      EMPTY_CREATE_FEEDBACK_RESPONSE,
      ENDPOINT,
    );
    expect(parsed).toMatchObject({
      id: "feedback-1",
      created_at: "2026-06-26T00:00:00Z",
      future_field: true,
    });
  });

  it("returns the empty fallback for malformed feedback responses", () => {
    expect(
      parseWithFallback(
        { id: 123, created_at: "2026-06-26T00:00:00Z" },
        CreateFeedbackResponseSchema,
        EMPTY_CREATE_FEEDBACK_RESPONSE,
        ENDPOINT,
      ),
    ).toBe(EMPTY_CREATE_FEEDBACK_RESPONSE);
    expect(
      parseWithFallback(null, CreateFeedbackResponseSchema, EMPTY_CREATE_FEEDBACK_RESPONSE, ENDPOINT),
    ).toBe(EMPTY_CREATE_FEEDBACK_RESPONSE);
  });
});

// `user.timezone` (Viewing tz) was added in the timezone-architecture RFC.
// A desktop build older than the server — or a server predating the
// `user.timezone` migration — will return a `/api/me` body with no
// `timezone` key. The schema must not fail closed on that: the field
// defaults to `null`, which the frontend resolves to the browser-detected
// tz at render time.
describe("UserSchema timezone drift", () => {
  const base = {
    id: "11111111-1111-1111-1111-111111111111",
    name: "Ada",
    email: "ada@example.com",
  };

  it("defaults timezone to null when the field is absent", () => {
    const parsed = UserSchema.parse(base);
    expect(parsed.timezone).toBe(null);
  });

  it("preserves an explicit IANA timezone", () => {
    const parsed = UserSchema.parse({ ...base, timezone: "Asia/Tokyo" });
    expect(parsed.timezone).toBe("Asia/Tokyo");
  });

  it("accepts an explicit null timezone", () => {
    const parsed = UserSchema.parse({ ...base, timezone: null });
    expect(parsed.timezone).toBe(null);
  });

  // Wrong-type drift: a future server bug sending `timezone` as a number
  // must not throw into the UI. parseWithFallback degrades the whole user
  // object to the explicit fallback (EMPTY_USER) so /api/me callers keep a
  // valid shape instead of white-screening.
  it("falls back to EMPTY_USER when timezone is the wrong type", () => {
    const parsed = parseWithFallback(
      { ...base, timezone: 42 },
      UserSchema,
      EMPTY_USER,
      { endpoint: "GET /api/me" },
    );
    expect(parsed).toBe(EMPTY_USER);
  });
});

// The workspace dashboard and runtime-detail pages were re-pointed at the
// unified `task_usage_hourly` rollup. Every numeric field drives chart /
// KPI math, and string keys (date / agent_id / model) bucket the series.
// The contract these schemas must hold: a row missing a field degrades
// that field to a sane default rather than dropping the WHOLE array to
// the `[]` fallback — one drifted row must not blank the entire chart.
describe("dashboard + runtime usage schema drift", () => {
  it("coerces a missing numeric field to 0 instead of dropping the array", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { date: "2026-05-19", model: "claude-opus-4-7", input_tokens: 100 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.output_tokens).toBe(0);
    expect(parsed[0]?.cache_read_tokens).toBe(0);
    expect(parsed[0]?.cache_write_tokens).toBe(0);
  });

  it("coerces a missing date key to \"\" so the rest of the series survives", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { model: "claude-opus-4-7", input_tokens: 5 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.date).toBe("");
  });

  it("coerces a missing agent_id key to \"\" for the agent-runtime panel", () => {
    const parsed = DashboardAgentRunTimeListSchema.parse([
      { total_seconds: 42, task_count: 3, failed_count: 0 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("defaults a missing cancelled_count to 0 so a pre-cancelled-count server still renders", () => {
    // cancelled_count was added when the run-time rollups started counting
    // runs the user stopped mid-flight. A backend predating it omits the
    // field; the row must survive with a 0 segment rather than drop the
    // whole series (installed desktop clients hit older backends).
    expect(
      DashboardAgentRunTimeListSchema.parse([
        { agent_id: "a", total_seconds: 42, task_count: 3, failed_count: 0 },
      ])[0]?.cancelled_count,
    ).toBe(0);
    expect(
      DashboardRunTimeDailyListSchema.parse([
        { date: "2026-05-19", total_seconds: 42, task_count: 3, failed_count: 0 },
      ])[0]?.cancelled_count,
    ).toBe(0);
  });

  it("preserves optional usage coverage without rejecting older or malformed rows", () => {
    const parsed = DashboardAgentRunTimeListSchema.parse([
      {
        agent_id: "new-server",
        total_seconds: 42,
        task_count: 3,
        metered_task_count: 2,
        failed_count: 0,
      },
      {
        agent_id: "old-server",
        total_seconds: 42,
        task_count: 3,
        failed_count: 0,
      },
      {
        agent_id: "drifted-server",
        total_seconds: 42,
        task_count: 3,
        metered_task_count: "not-a-number",
        failed_count: 0,
      },
    ]);
    expect(parsed[0]?.metered_task_count).toBe(2);
    expect(parsed[1]?.metered_task_count).toBeUndefined();
    expect(parsed[2]?.metered_task_count).toBeUndefined();
  });

  it("coerces a missing agent_id key to \"\" for the usage-by-agent panel", () => {
    const parsed = DashboardUsageByAgentListSchema.parse([
      { model: "claude-opus-4-7", input_tokens: 7 },
    ]);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("coerces missing fields on every runtime usage schema", () => {
    expect(RuntimeUsageListSchema.parse([{ date: "2026-05-19" }])[0]?.input_tokens).toBe(0);
    expect(RuntimeHourlyActivityListSchema.parse([{ hour: 9 }])[0]?.count).toBe(0);
    expect(RuntimeUsageByAgentListSchema.parse([{ model: "x" }])[0]?.agent_id).toBe("");
    expect(RuntimeUsageByHourListSchema.parse([{ hour: 9 }])[0]?.model).toBe("");
  });

  it("defaults a missing provider to \"\" so an older server's rows still price by bare model", () => {
    // provider was added for cross-provider model disambiguation; a server
    // predating it omits the field. The schema must fill "" (→ bare-model
    // pricing lookup) rather than drop the row.
    expect(
      DashboardUsageDailyListSchema.parse([{ date: "2026-05-19", model: "claude-opus-4-7" }])[0]
        ?.provider,
    ).toBe("");
    expect(
      DashboardUsageByAgentListSchema.parse([{ model: "claude-opus-4-7" }])[0]?.provider,
    ).toBe("");
    expect(RuntimeUsageByAgentListSchema.parse([{ model: "x" }])[0]?.provider).toBe("");
  });

  it("rejects a non-array body so parseWithFallback can return its fallback", () => {
    expect(DashboardUsageDailyListSchema.safeParse(null).success).toBe(false);
    expect(DashboardFailureDailyListSchema.safeParse(null).success).toBe(false);
    expect(DashboardFailureByAgentListSchema.safeParse({ rows: [] }).success).toBe(
      false,
    );
    expect(RuntimeUsageListSchema.safeParse({ rows: [] }).success).toBe(false);
  });

  it("keeps a failure_reason the client build has never heard of", () => {
    // failure_reason is an open string, not an enum: the backend taxonomy
    // grows, and an installed desktop client must still count a reason its
    // build predates rather than dropping the row (and with it the day's
    // error total).
    const parsed = DashboardFailureDailyListSchema.parse([
      { date: "2026-05-19", failure_reason: "agent_error.brand_new", task_count: 3 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.failure_reason).toBe("agent_error.brand_new");
    expect(parsed[0]?.task_count).toBe(3);
  });

  it("coerces a missing failure row field without dropping the array", () => {
    const daily = DashboardFailureDailyListSchema.parse([{ date: "2026-05-19" }]);
    expect(daily).toHaveLength(1);
    // "" is the succeeded bucket, so a reason-less row lands in the
    // denominator instead of inventing a failure that never happened.
    //
    // Defaulting to a failure bucket instead was considered and rejected: the
    // realistic drift here is someone adding `omitempty` to the Go struct
    // tag, which would strip the field from exactly the SUCCESS rows and turn
    // every window into a 100% error rate. Deflating a rate under drift is
    // the milder failure. TestDashboardFailureWireContractKeepsEmptyReason
    // (server/internal/handler/dashboard_test.go) guards the other side by
    // pinning that the server always emits the field.
    expect(daily[0]?.failure_reason).toBe("");
    expect(daily[0]?.task_count).toBe(0);

    const byAgent = DashboardFailureByAgentListSchema.parse([
      { failure_reason: "timeout", task_count: 2 },
    ]);
    expect(byAgent[0]?.agent_id).toBe("");
  });

  it("keeps unknown server-side fields via .loose()", () => {
    const parsed = RuntimeUsageListSchema.parse([
      { date: "2026-05-19", region: "us-east" },
    ]);
    expect((parsed[0] as Record<string, unknown>).region).toBe("us-east");
  });
});

describe("AppConfigSchema signup_totp_required drift", () => {
  it.each([
    [undefined, false],
    ["yes", false],
    [true, true],
  ])("%j parses as %s", (value, expected) => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      signup_totp_required: value,
    });
    expect(parsed.signup_totp_required).toBe(expected);
  });
});

// A server that never heard of worktree mode also never sends this flag, and
// it does not reject the mode either — it drops execution_mode and answers 201,
// leaving the task to run in the user's working copy (#7113). So the absent
// case has to parse as false, not as "unknown, probably fine".
// An older server deletes a comment's replies with it and omits this field,
// so absent or malformed must parse as false: the client then promises nothing
// about replies and keeps the legacy delete route (#8296).
describe("AppConfigSchema comment_delete_keep_replies_supported drift", () => {
  it.each([
    [undefined, false],
    ["yes", false],
    [true, true],
  ])("%j parses as %s", (value, expected) => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      comment_delete_keep_replies_supported: value,
    });
    expect(parsed.comment_delete_keep_replies_supported).toBe(expected);
  });
});

describe("AppConfigSchema local_worktree_supported drift", () => {
  it("defaults to false when the server predates the signal", () => {
    const parsed = AppConfigSchema.parse({ cdn_domain: "cdn.example.com" });
    expect(parsed.local_worktree_supported).toBe(false);
  });

  it("coerces a malformed value to false rather than trusting it", () => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      local_worktree_supported: "yes",
    });
    expect(parsed.local_worktree_supported).toBe(false);
  });

  it("carries a genuine true through", () => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      local_worktree_supported: true,
    });
    expect(parsed.local_worktree_supported).toBe(true);
  });
});

describe("AppConfigSchema agent_conversation_starters_supported drift", () => {
  it("defaults to false when the server predates the persistence contract", () => {
    expect(AppConfigSchema.parse({}).agent_conversation_starters_supported).toBe(false);
  });

  it("coerces a malformed declaration to false", () => {
    expect(
      AppConfigSchema.parse({ agent_conversation_starters_supported: "yes" })
        .agent_conversation_starters_supported,
    ).toBe(false);
  });

  it("carries a genuine declaration through", () => {
    expect(
      AppConfigSchema.parse({ agent_conversation_starters_supported: true })
        .agent_conversation_starters_supported,
    ).toBe(true);
  });
});

describe("AppConfigSchema issue_create_properties_supported drift", () => {
  it("defaults to false when the server predates atomic create properties", () => {
    expect(AppConfigSchema.parse({}).issue_create_properties_supported).toBe(false);
  });

  it("coerces a malformed declaration to false", () => {
    expect(
      AppConfigSchema.parse({ issue_create_properties_supported: "yes" })
        .issue_create_properties_supported,
    ).toBe(false);
  });

  it("carries a genuine declaration through", () => {
    expect(
      AppConfigSchema.parse({ issue_create_properties_supported: true })
        .issue_create_properties_supported,
    ).toBe(true);
  });
});

describe("AppConfigSchema cdn_signed drift", () => {
  it("defaults cdn_signed to false when the server omits it (pre-MUL-3254 servers)", () => {
    const parsed = AppConfigSchema.parse({ cdn_domain: "cdn.example.com" });
    expect(parsed.cdn_signed).toBe(false);
  });

  it("coerces a malformed cdn_signed to false instead of failing the whole config", () => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      cdn_signed: "yes",
    });
    expect(parsed.cdn_signed).toBe(false);
    expect(parsed.cdn_domain).toBe("cdn.example.com");
  });

  it("keeps cdn_signed=true from a signing-enabled server", () => {
    const parsed = AppConfigSchema.parse({ cdn_signed: true });
    expect(parsed.cdn_signed).toBe(true);
  });

  it("parses frontend feature flag decisions", () => {
    const parsed = AppConfigSchema.parse({
      feature_flags: {
        plugins_v1: true,
        malformed_future_flag: "yes",
      },
    });
    expect(parsed.feature_flags).toEqual({
      plugins_v1: true,
      malformed_future_flag: false,
    });
  });

  it("defaults malformed feature_flags to an empty object", () => {
    const parsed = AppConfigSchema.parse({ feature_flags: ["not", "an", "object"] });
    expect(parsed.feature_flags).toEqual({});
  });

  it("parses server_version and leaves it undefined when the server omits it", () => {
    expect(AppConfigSchema.parse({ server_version: "1.2.3" }).server_version).toBe("1.2.3");
    expect(AppConfigSchema.parse({}).server_version).toBeUndefined();
  });
});

describe("InboxUnreadSummarySchema", () => {
  const ENDPOINT = { endpoint: "GET /api/inbox/unread-summary" };

  it("parses a well-formed summary and tolerates extra fields", () => {
    const parsed = parseWithFallback(
      [
        { workspace_id: "ws-1", count: 2 },
        { workspace_id: "ws-2", count: 0, future_field: "ignored" },
      ],
      InboxUnreadSummarySchema,
      EMPTY_INBOX_UNREAD_SUMMARY,
      ENDPOINT,
    );
    expect(parsed).toEqual([
      { workspace_id: "ws-1", count: 2 },
      { workspace_id: "ws-2", count: 0, future_field: "ignored" },
    ]);
  });

  it("returns the empty fallback (dot hidden) for a non-array body", () => {
    expect(
      parseWithFallback({ rows: [] }, InboxUnreadSummarySchema, EMPTY_INBOX_UNREAD_SUMMARY, ENDPOINT),
    ).toBe(EMPTY_INBOX_UNREAD_SUMMARY);
    expect(
      parseWithFallback(null, InboxUnreadSummarySchema, EMPTY_INBOX_UNREAD_SUMMARY, ENDPOINT),
    ).toBe(EMPTY_INBOX_UNREAD_SUMMARY);
  });

  it("returns the empty fallback when an entry has a wrong-typed count", () => {
    expect(
      parseWithFallback(
        [{ workspace_id: "ws-1", count: "lots" }],
        InboxUnreadSummarySchema,
        EMPTY_INBOX_UNREAD_SUMMARY,
        ENDPOINT,
      ),
    ).toBe(EMPTY_INBOX_UNREAD_SUMMARY);
  });
});

describe("InboxItemListSchema", () => {
  const ENDPOINT = { endpoint: "GET /api/inbox/archived" };

  const row = (overrides: Record<string, unknown> = {}) => ({
    id: "inbox-1",
    workspace_id: "ws-1",
    recipient_type: "member",
    recipient_id: "member-1",
    type: "new_comment",
    severity: "info",
    issue_id: "issue-1",
    title: "Issue title",
    body: null,
    read: false,
    archived: true,
    created_at: "2026-06-15T08:00:00Z",
    ...overrides,
  });

  it("parses a well-formed archived list and tolerates extra fields", () => {
    const parsed = parseWithFallback(
      [row({
        issue_status: "in_progress",
        issue_priority: "high",
        details: { comment_id: "c-1" },
        future_field: 1,
      })],
      InboxItemListSchema,
      EMPTY_INBOX_ITEMS,
      ENDPOINT,
    );
    expect(parsed).toHaveLength(1);
    expect(parsed[0]).toMatchObject({
      id: "inbox-1",
      archived: true,
      issue_status: "in_progress",
      issue_priority: "high",
    });
  });

  it("keeps a notification type this client doesn't know yet", () => {
    // Enums stay lenient on purpose: a backend that ships a new inbox type
    // must not blank the whole archived list on older clients.
    const parsed = parseWithFallback(
      [row({ type: "some_future_type", severity: "future_severity" })],
      InboxItemListSchema,
      EMPTY_INBOX_ITEMS,
      ENDPOINT,
    );
    expect(parsed).toHaveLength(1);
  });

  it("accepts rows that omit the nullable optional fields", () => {
    const { body, issue_id, ...withoutOptionals } = row();
    void body;
    void issue_id;
    expect(
      parseWithFallback([withoutOptionals], InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toHaveLength(1);
  });

  it("returns the empty fallback when an issue projection is wrong-typed", () => {
    expect(
      parseWithFallback(
        [row({ issue_priority: 3 })],
        InboxItemListSchema,
        EMPTY_INBOX_ITEMS,
        ENDPOINT,
      ),
    ).toBe(EMPTY_INBOX_ITEMS);
  });

  it("returns the empty fallback for a non-array body", () => {
    expect(
      parseWithFallback({ items: [] }, InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toBe(EMPTY_INBOX_ITEMS);
    expect(
      parseWithFallback(null, InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toBe(EMPTY_INBOX_ITEMS);
  });

  it("returns the empty fallback when a row is missing a required field", () => {
    const { id, ...withoutId } = row();
    void id;
    expect(
      parseWithFallback([withoutId], InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toBe(EMPTY_INBOX_ITEMS);
  });

  it("returns the empty fallback when `archived` is wrong-typed", () => {
    expect(
      parseWithFallback(
        [row({ archived: "yes" })],
        InboxItemListSchema,
        EMPTY_INBOX_ITEMS,
        ENDPOINT,
      ),
    ).toBe(EMPTY_INBOX_ITEMS);
  });
});

describe("SearchProjectsResponseSchema date drift", () => {
  const ENDPOINT = { endpoint: "GET /api/projects/search" };

  const baseProject = {
    id: "p-1",
    workspace_id: "ws-1",
    title: "Launch",
    description: null,
    icon: null,
    status: "in_progress",
    priority: "high",
    lead_type: null,
    lead_id: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    match_source: "title",
  };

  it("parses start_date / due_date when the backend returns them", () => {
    const parsed = parseWithFallback(
      { projects: [{ ...baseProject, start_date: "2026-03-01", due_date: "2026-03-31" }], total: 1 },
      SearchProjectsResponseSchema,
      EMPTY_SEARCH_PROJECTS_RESPONSE,
      ENDPOINT,
    );
    expect(parsed.projects[0]?.start_date).toBe("2026-03-01");
    expect(parsed.projects[0]?.due_date).toBe("2026-03-31");
  });

  // Frontend deploys before backend: an older backend omits the new keys. The
  // .default(null) must keep the whole batch parseable (→ null), not degrade
  // it to the empty fallback and blank the search results.
  it("defaults missing start_date / due_date to null without dropping results", () => {
    const parsed = parseWithFallback(
      { projects: [baseProject], total: 1 },
      SearchProjectsResponseSchema,
      EMPTY_SEARCH_PROJECTS_RESPONSE,
      ENDPOINT,
    );
    expect(parsed).not.toBe(EMPTY_SEARCH_PROJECTS_RESPONSE);
    expect(parsed.projects).toHaveLength(1);
    expect(parsed.projects[0]?.start_date).toBeNull();
    expect(parsed.projects[0]?.due_date).toBeNull();
  });
});

// The comment composer branches on preview.blocked to warn before sending
// (MUL-4525 §2), so the additive field must parse and degrade gracefully.
describe("CommentTriggerPreviewSchema.blocked", () => {
  it("parses blocked mention outcomes alongside agents", () => {
    const parsed = CommentTriggerPreviewSchema.parse({
      agents: [{ id: "a1", source: "mention_agent", reason: "" }],
      blocked: [
        { target_type: "agent", target_id: "s1", status: "blocked", reason_code: "invocation_not_allowed" },
      ],
    });
    expect(parsed.agents).toHaveLength(1);
    expect(parsed.blocked).toEqual([
      { target_type: "agent", target_id: "s1", status: "blocked", reason_code: "invocation_not_allowed" },
    ]);
  });

  it("defaults blocked to [] when an older server omits it", () => {
    const parsed = CommentTriggerPreviewSchema.parse({ agents: [] });
    expect(parsed.blocked).toEqual([]);
  });

  it("degrades a malformed blocked field to [] without dropping agents", () => {
    const parsed = CommentTriggerPreviewSchema.parse({
      agents: [{ id: "a1", source: "mention_agent", reason: "" }],
      blocked: "nope",
    });
    expect(parsed.agents).toHaveLength(1);
    expect(parsed.blocked).toEqual([]);
  });

  it("drops a single malformed blocked entry without discarding the valid ones", () => {
    const parsed = CommentTriggerPreviewSchema.parse({
      agents: [],
      blocked: [
        { target_type: "agent", target_id: "s1", status: "blocked", reason_code: "invocation_not_allowed" },
        { status: "blocked" }, // missing target_id → dropped individually
        { target_type: "agent", target_id: "a1", status: "blocked", reason_code: "runtime_offline" },
      ],
    });
    expect(parsed.blocked.map((b) => b.target_id)).toEqual(["s1", "a1"]);
  });
});

describe("RuntimeModelListRequestSchema", () => {
  const completed = {
    id: "req-1",
    runtime_id: "rt-1",
    status: "completed",
    supported: true,
    created_at: "2026-07-29T00:00:00Z",
    updated_at: "2026-07-29T00:00:01Z",
    models: [
      {
        id: "gpt-5.6-sol",
        label: "GPT-5.6-Sol",
        provider: "openai",
        default: true,
        thinking: {
          supported_levels: [{ value: "high", label: "High" }],
          default_level: "low",
        },
        service_tiers: [{ id: "fast", name: "Fast" }],
        supports_explicit_standard_service_tier: true,
      },
    ],
  };

  it("parses a live completed discovery, keeping the fields the UI branches on", () => {
    const parsed = parseWithFallback(
      completed,
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.status).toBe("completed");
    expect(parsed.supported).toBe(true);
    expect(parsed.models?.[0]?.default).toBe(true);
    expect(parsed.models?.[0]?.thinking?.supported_levels).toEqual([
      { value: "high", label: "High" },
    ]);
    expect(parsed.models?.[0]?.service_tiers).toEqual([{ id: "fast", name: "Fast" }]);
    expect(
      parsed.models?.[0]?.supports_explicit_standard_service_tier,
    ).toBe(true);
    expect(parsed.cached).toBeUndefined();
  });

  it("keeps the additive cache markers when the server serves a snapshot", () => {
    const parsed = parseWithFallback(
      { ...completed, cached: true, cached_at: "2026-07-29T00:00:00Z" },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.cached).toBe(true);
    expect(parsed.cached_at).toBe("2026-07-29T00:00:00Z");
  });

  // A backend that predates MUL-5444 sends neither marker; an even older one
  // may omit `supported`. Both must stay usable rather than reading as
  // "runtime manages the model itself" off an undefined.
  it("defaults supported to true on an older backend that omits it", () => {
    const { supported: _omitted, ...withoutSupported } = completed;
    const parsed = parseWithFallback(
      withoutSupported,
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.supported).toBe(true);
    expect(parsed.cached).toBeUndefined();
  });

  // MUL-6961: models the runtime cannot run arrive in their OWN list. The
  // separation is the compatibility contract — a client that never learned the
  // field reads `models` and therefore cannot offer one — so the schema must
  // keep them apart rather than folding them together.
  it("parses unavailable models without letting them into the selectable list", () => {
    const parsed = parseWithFallback(
      {
        ...completed,
        unavailable_models: [
          {
            id: "cc-update-required-1",
            label: "Fable 5.1 (disabled)",
            reason: "Update to 2.1.255+ to use Fable 5.1",
          },
        ],
      },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.unavailable_models?.[0]?.id).toBe("cc-update-required-1");
    expect(parsed.unavailable_models?.[0]?.reason).toBe(
      "Update to 2.1.255+ to use Fable 5.1",
    );
    expect(parsed.models?.map((m) => m.id)).not.toContain(
      "cc-update-required-1",
    );
  });

  // An older daemon or server sends no such key. The picker then shows no
  // unavailable section, which is exactly the pre-MUL-6961 behaviour.
  it("tolerates a backend that omits unavailable models entirely", () => {
    const parsed = parseWithFallback(
      completed,
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.unavailable_models).toBeUndefined();
    expect(parsed.models?.length).toBe(1);
  });

  it("treats an older daemon that omits explicit-standard support as unsupported", () => {
    const model = completed.models[0]!;
    const {
      supports_explicit_standard_service_tier: _omitted,
      ...oldDaemonModel
    } = model;
    const parsed = parseWithFallback(
      { ...completed, models: [oldDaemonModel] },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );

    expect(
      parsed.models?.[0]?.supports_explicit_standard_service_tier,
    ).toBeUndefined();
  });

  it("passes an unknown status through instead of failing the whole response", () => {
    const parsed = parseWithFallback(
      { ...completed, status: "superseded" },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.status).toBe("superseded");
  });

  // Malformed bodies must land on the "failed" fallback: `completed` would
  // fabricate an empty catalog and `pending` would spin the picker until the
  // client-side poll timeout.
  it("falls back to an explicit failure on a malformed body", () => {
    for (const malformed of [
      null,
      "nope",
      42,
      {},
      { status: 7 },
      { ...completed, status: undefined },
      { ...completed, supported: "yes" },
      { ...completed, models: "nope" },
      { ...completed, models: [{ label: "no id" }] },
      {
        ...completed,
        models: [
          {
            ...completed.models[0],
            supports_explicit_standard_service_tier: "yes",
          },
        ],
      },
    ]) {
      const parsed = parseWithFallback(
        malformed,
        RuntimeModelListRequestSchema,
        MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
        { endpoint: "test" },
      );
      expect(parsed.status).toBe("failed");
      expect(parsed.supported).toBe(true);
      expect(parsed.error).toBe("invalid model discovery response");
    }
  });

  it("keeps unknown server fields instead of stripping them", () => {
    const parsed = parseWithFallback(
      { ...completed, future_field: "keep me" },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect((parsed as unknown as { future_field?: string }).future_field).toBe(
      "keep me",
    );
  });
});

describe("IssueViewSchema", () => {
  const valid = {
    id: "v1",
    workspace_id: "ws1",
    owner_id: "u1",
    name: "Needs review",
    scope_type: "workspace",
    scope_id: null,
    scope_variant: null,
    visibility: "workspace",
    definition_version: 1,
    query: { statusFilters: ["in_review"] },
    display: { viewMode: "board" },
    revision: 3,
    created_at: "2026-08-06T00:00:00Z",
    updated_at: "2026-08-06T00:00:00Z",
  };

  it("parses a well-formed view and keeps unknown future fields", () => {
    const parsed = IssueViewSchema.parse({ ...valid, future_field: "keep me" });
    expect(parsed.name).toBe("Needs review");
    expect(parsed.query).toEqual({ statusFilters: ["in_review"] });
    expect((parsed as unknown as { future_field?: string }).future_field).toBe("keep me");
  });

  it("defaults missing definition blobs instead of failing", () => {
    const parsed = IssueViewSchema.parse({ id: "v2" });
    expect(parsed.query).toEqual({});
    expect(parsed.display).toEqual({});
    expect(parsed.revision).toBe(1);
  });

  it("degrades a malformed list response to [] via parseWithFallback", () => {
    expect(
      parseWithFallback({ nonsense: true }, IssueViewListSchema, [], {
        endpoint: "GET /api/issue-views",
      }),
    ).toEqual([]);
    expect(
      parseWithFallback(null, IssueViewListSchema, [], {
        endpoint: "GET /api/issue-views",
      }),
    ).toEqual([]);
  });

  it("degrades a malformed detail response to null — NOT an error", () => {
    // The sidebar's pinned view rows hinge on this distinction: a parse
    // fallback (null, no error) hides the row, while only a REAL 404
    // error may ever unpin. A malformed body must never destroy a pin.
    expect(
      parseWithFallback({ nonsense: true }, IssueViewSchema.nullable(), null, {
        endpoint: "GET /api/issue-views/{id}",
      }),
    ).toBeNull();
  });
});

// Issue status catalog (MUL-6243). The catalog drives how every status renders,
// so a drifting or malformed response must degrade to the built-ins rather than
// leaving the UI with no statuses at all.
describe("issue status catalog schemas", () => {
  const baseStatus = {
    id: "status-1",
    workspace_id: "ws-1",
    key: "human_review",
    name: "Human Review",
    description: "Waiting on a person",
    category: "in_review",
    color: "#22c55e",
    is_system: false,
    position: 1,
    archived_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("parses a full catalog response", () => {
    const parsed = ListIssueStatusesResponseSchema.parse({
      statuses: [baseStatus],
      categories: ["backlog", "todo", "in_progress", "in_review", "blocked", "done", "cancelled"],
      total: 1,
    });
    expect(parsed.statuses[0]?.key).toBe("human_review");
    expect(parsed.statuses[0]?.category).toBe("started");
    expect(parsed.categories).toHaveLength(4);
  });

  it("falls back to the built-in categories on a malformed response", () => {
    const parsed = parseWithFallback(
      { statuses: "not-an-array", categories: 7 },
      ListIssueStatusesResponseSchema,
      EMPTY_LIST_ISSUE_STATUSES_RESPONSE,
      { endpoint: "GET /api/issue-statuses" },
    );
    expect(parsed).toEqual(EMPTY_LIST_ISSUE_STATUSES_RESPONSE);
    // The fallback still names all 5 lifecycle categories, so a malformed
    // response cannot leave grouped issue surfaces without columns.
    expect(parsed.categories).toHaveLength(4);
    expect(parsed.statuses).toEqual([]);
  });

  it("defaults the optional presentation fields the server may omit", () => {
    const { color: _c, is_system: _s, position: _p, description: _d, archived_at: _a, ...minimal } = baseStatus;
    const parsed = IssueStatusEntrySchema.parse(minimal);
    expect(parsed.color).toBe("#6b7280");
    expect(parsed.is_system).toBe(false);
    expect(parsed.position).toBe(0);
    expect(parsed.archived_at).toBeNull();
  });

  it.each([undefined, null, "", "three_quarters", "future-icon"])("keeps catalog readable with icon %s", (icon) => {
    const parsed = IssueStatusEntrySchema.parse({ ...baseStatus, icon });
    expect(parsed.key).toBe(baseStatus.key);
    expect(parsed.icon).toBe(icon);
  });

  // PATCH /api/issue-statuses/reorder returns the same catalog shape as the
  // list endpoint, so a malformed reorder response degrades the same way rather
  // than leaving the settings page holding an unparsed blob. (MUL-6243)
  it("falls back on a malformed reorder response", () => {
    const parsed = parseWithFallback(
      { statuses: [{ id: 1 }], total: "many" },
      ListIssueStatusesResponseSchema,
      EMPTY_LIST_ISSUE_STATUSES_RESPONSE,
      { endpoint: "PATCH /api/issue-statuses/reorder" },
    );
    expect(parsed).toEqual(EMPTY_LIST_ISSUE_STATUSES_RESPONSE);
  });

  it("keeps an unknown category as a string instead of failing the whole catalog", () => {
    // A newer server could report a category this build does not know. Dropping
    // the entry (or throwing) would leave the board unable to render issues
    // already sitting on it, so the value is carried through verbatim.
    const parsed = ListIssueStatusesResponseSchema.parse({
      statuses: [{ ...baseStatus, category: "started" }],
      categories: ["started"],
      total: 1,
    });
    expect(parsed.statuses[0]?.category).toBe("started");
  });

  it("falls back to an empty entry on a malformed single status", () => {
    const parsed = parseWithFallback(
      { id: 12345 },
      IssueStatusEntrySchema,
      EMPTY_ISSUE_STATUS_ENTRY,
      { endpoint: "POST /api/issue-statuses" },
    );
    expect(parsed).toEqual(EMPTY_ISSUE_STATUS_ENTRY);
  });
});

describe("TaskMessageListSchema", () => {
  it("preserves call IDs and tolerates old or malformed optional identity", () => {
    const base = { task_id: "task-1", seq: 1, type: "tool_result", output: "ok" };
    const parsed = parseWithFallback<{ call_id?: string; output?: string }[]>(
      [
        { ...base, call_id: "execution:A" },
        base,
        { ...base, call_id: null },
        { ...base, call_id: 42 },
        { ...base, call_id: {} },
      ],
      TaskMessageListSchema, [], { endpoint: "GET /api/tasks/:id/messages" },
    );
    expect(parsed).toHaveLength(5);
    expect(parsed.map((m) => m.call_id)).toEqual(["execution:A", undefined, undefined, undefined, undefined]);
    expect(parsed.every((m) => m.output === "ok")).toBe(true);
  });

  const row = { task_id: "task-1", issue_id: "issue-1", seq: 1, type: "tool_result", output: "log line" };

  // The whole point of the field: a server that never sends it is saying
  // "nobody measured this", and only `undefined` can carry that. A default of
  // false would make every historical row assert it is complete.
  it("leaves a missing truncation flag undefined rather than false", () => {
    const parsed = TaskMessageListSchema.parse([row]);
    expect(parsed[0]).not.toHaveProperty("output_truncated", false);
    expect(parsed[0]?.output_truncated).toBeUndefined();
  });

  it("keeps both measured values", () => {
    const parsed = TaskMessageListSchema.parse([
      { ...row, seq: 1, output_truncated: true },
      { ...row, seq: 2, output_truncated: false },
    ]);
    expect(parsed.map((m) => m.output_truncated)).toEqual([true, false]);
  });

  // Drift defense. Without a field-level catch, one bad boolean fails its row,
  // the array fails with it, and parseWithFallback hands the viewer an empty
  // transcript — a malformed flag would delete the whole run from the screen.
  // Degrading the field to "unknown" is the correct loss.
  it("keeps the record and forgets the field when the flag is malformed", () => {
    const parsed = parseWithFallback<{ output?: string; output_truncated?: boolean }[]>(
      [{ ...row, output_truncated: "false" }],
      TaskMessageListSchema,
      [],
      { endpoint: "GET /api/tasks/:id/messages" },
    );
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.output).toBe("log line");
    expect(parsed[0]?.output_truncated).toBeUndefined();
  });

  it("keeps the surrounding rows when one row's flag is malformed", () => {
    const parsed = TaskMessageListSchema.parse([
      { ...row, seq: 1, output_truncated: true },
      { ...row, seq: 2, output_truncated: 12345 },
      { ...row, seq: 3, output_truncated: false },
    ]);
    expect(parsed.map((m) => m.seq)).toEqual([1, 2, 3]);
    expect(parsed.map((m) => m.output_truncated)).toEqual([true, undefined, false]);
  });

  it("falls back to an empty transcript when the response is not a list", () => {
    const parsed = parseWithFallback(
      { messages: "nope" },
      TaskMessageListSchema,
      [],
      { endpoint: "GET /api/tasks/:id/messages" },
    );
    expect(parsed).toEqual([]);
  });

  it("downgrades an unknown message type instead of dropping the transcript", () => {
    const parsed = TaskMessageListSchema.parse([{ ...row, type: "video" }]);
    expect(parsed[0]?.type).toBe("text");
  });
});


describe("AgentActivityBucketListSchema duration", () => {
  const bucket = { agent_id: "a", bucket_at: "2026-09-24T00:00:00Z", task_count: 201,
    completed_count: 201, failed_count: 0, cancelled_count: 0 };
  it("accepts optional aggregate duration from new and old servers", () => {
    expect(AgentActivityBucketListSchema.parse([bucket])[0]?.duration_ms).toBeUndefined();
    expect(AgentActivityBucketListSchema.parse([{ ...bucket, duration_ms: 12600000, duration_count: 201 }])[0]?.duration_count).toBe(201);
  });
  it("does not discard activity counts when duration is malformed", () => {
    const parsed = AgentActivityBucketListSchema.parse([{ ...bucket, duration_ms: "slow", duration_count: -1 }]);
    expect(parsed[0]?.task_count).toBe(201);
    expect(parsed[0]?.duration_ms).toBeUndefined();
    expect(parsed[0]?.duration_count).toBeUndefined();
  });
});

describe("WorkspaceBotSchema", () => {
  const bot = { id: "b1", workspace_id: "w", user_id: "u", name: "Grok", can_release: true,
    webhook_url: "https://x/hook", webhook_failures: 0, webhook_last_error: "",
    token_prefix: "mul_ab12", token_last_used_at: null, created_at: "2026-10-13T00:00:00Z" };
  it("keeps a bot whose optional fields are malformed or missing", () => {
    const parsed = WorkspaceBotListSchema.parse([{ id: "b1", name: "Grok", can_release: "yes", webhook_failures: -3, token_prefix: 7 }]);
    expect(parsed[0]).toMatchObject({ name: "Grok", can_release: false, webhook_failures: 0, token_prefix: null, webhook_url: "" });
  });
  it("carries secrets only when the server sends them", () => {
    expect(WorkspaceBotWithSecretsSchema.parse(bot).token).toBeUndefined();
    expect(WorkspaceBotWithSecretsSchema.parse({ ...bot, token: "mul_x", webhook_secret: "s", setup_prompt: "p" })).toMatchObject({ token: "mul_x", webhook_secret: "s", setup_prompt: "p" });
  });
  it("falls back to an empty list for a non-array reply", () => {
    expect(parseWithFallback({ bots: [] }, WorkspaceBotListSchema, [], { endpoint: "GET /api/bots" })).toEqual([]);
  });
});

describe("plan and meeting schemas", () => {
  const plan = {
    id: "plan-1",
    issue_id: "issue-1",
    version: 2,
    status: "proposed",
    goal: "Ship it",
    steps: [{ title: "Build", assignee_type: "agent", assignee_id: "a-1", issue_id: null }],
    comment_id: "c-1",
  };

  it("keeps good plans and defaults missing approval to false", () => {
    const parsed = IssuePlansSchema.parse({ plans: [plan] });
    expect(parsed.can_approve).toBe(false);
    expect(parsed.plans[0]?.steps[0]?.title).toBe("Build");
    expect(parsed.plans[0]?.approved_by).toBeNull();
  });

  it("falls back on malformed plan payloads without hiding the card", () => {
    const parsed = IssuePlansSchema.parse({
      plans: [{ ...plan, steps: "nope", version: "x" }],
      can_approve: "yes",
    });
    expect(parsed.can_approve).toBe(false);
    expect(parsed.plans[0]?.steps).toEqual([]);
    expect(parsed.plans[0]?.version).toBe(0);
    expect(
      parseWithFallback("garbage", IssuePlansSchema, EMPTY_ISSUE_PLANS, {
        endpoint: "GET /api/issues/{id}/plans",
      }),
    ).toEqual(EMPTY_ISSUE_PLANS);
  });

  it("parses meetings with a missing plan and bad counters", () => {
    const parsed = MeetingListSchema.parse({
      meetings: [
        { issue: { id: "i-1", title: "Q" }, plan: "bad", steps_total: -1, steps_done: "2" },
      ],
    });
    expect(parsed.meetings[0]?.plan).toBeNull();
    expect(parsed.meetings[0]?.steps_total).toBe(0);
    expect(parsed.meetings[0]?.steps_done).toBe(0);
    expect(
      parseWithFallback(42, MeetingListSchema, { meetings: EMPTY_MEETINGS }, {
        endpoint: "GET /api/meetings",
      }).meetings,
    ).toEqual([]);
  });

  it("defaults approver fields and survives a non-object reply", () => {
    const parsed = PlanApproverListSchema.parse({ approvers: [{ user_id: "u-1" }] });
    expect(parsed.approvers[0]).toEqual({ user_id: "u-1", name: "", granted_by: "", created_at: "" });
    expect(PlanApproverListSchema.parse({ approvers: "x" }).approvers).toEqual([]);
  });
});
