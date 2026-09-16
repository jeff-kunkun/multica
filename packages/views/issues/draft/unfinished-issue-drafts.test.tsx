import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftSummary } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import {
  UnfinishedIssueDraftsBanner,
  issueDraftPreview,
  issueDraftTitle,
} from "./unfinished-issue-drafts";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

/**
 * The drafts list is the ONLY route back into an alignment conversation — its
 * carrier is a `kind='system'` agent, so it is absent from every chat list. A
 * row whose title or preview renders the carrier's wire format is a row nobody
 * can recognise, which is the same as no list at all.
 */

function summary(overrides: Partial<IssueDraftSummary>): IssueDraftSummary {
  return {
    chat_session_id: "sess-1",
    workspace_id: "ws-1",
    status: "draft",
    revision: 1,
    draft: { title: "", description: "", status: "", priority: "" },
    issue_id: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    title: "Align a new issue",
    runtime_id: "rt-1",
    last_message_content: "",
    last_message_role: "",
    last_message_at: "",
    ...overrides,
  };
}

describe("issueDraftTitle", () => {
  it("uses the draft's own title, not the constant chat session title", () => {
    // Every carrier session is titled "Align a new issue"; a list of those is
    // unreadable, so the structured title is the only candidate worth showing.
    expect(
      issueDraftTitle(summary({ draft: { title: "Dark mode", description: "", status: "", priority: "" } })),
    ).toBe("Dark mode");
    expect(issueDraftTitle(summary({}))).toBe("");
  });
});

describe("issueDraftPreview", () => {
  it("decodes the user's own turn out of the wire envelope", () => {
    const content =
      'MULTICA_ISSUE_DRAFT_INPUT\n{"user_request":"add dark mode","current_draft":{"title":"","description":"","status":"","priority":""}}';
    expect(
      issueDraftPreview(summary({ last_message_role: "user", last_message_content: content })),
    ).toBe("add dark mode");
  });

  it("strips the carrier's draft block from an assistant turn", () => {
    expect(
      issueDraftPreview(
        summary({
          last_message_role: "assistant",
          last_message_content:
            'Which surfaces?\n<issue_draft>{"title":"Dark mode"}</issue_draft>',
        }),
      ),
    ).toBe("Which surfaces?");
  });
});

describe("UnfinishedIssueDraftsBanner", () => {
  const one = summary({ chat_session_id: "sess-1", draft: { title: "Dark mode", description: "", status: "", priority: "" } });

  it("renders nothing when there is no unfinished draft", () => {
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <UnfinishedIssueDraftsBanner drafts={[]} onResume={() => {}} />
      </I18nProvider>,
    );
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("names the draft by its structured title", () => {
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <UnfinishedIssueDraftsBanner drafts={[one]} onResume={() => {}} />
      </I18nProvider>,
    );
    expect(screen.getByRole("button").textContent).toContain("1 unfinished alignment");
  });
});
