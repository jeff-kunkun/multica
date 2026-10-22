// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";
import type { InheritedPromptState } from "../../specialization";
import { InstructionsTab } from "./instructions-tab";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Reviewer",
  description: "",
  instructions: "Review carefully.",
  conversation_starters: [{ label: "Review a PR", prompt: "Review it." }],
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-08-24T00:00:00Z",
  updated_at: "2026-08-24T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function tab(
  agent: Agent,
  onSave = vi.fn().mockResolvedValue(undefined),
  extra: {
    inheritedPromptState?: InheritedPromptState;
    onRetryInheritedPrompt?: () => void;
  } = {},
) {
  return <InstructionsTab agent={agent} onSave={onSave} {...extra} />;
}

async function typeInstructions(text: string) {
  const user = userEvent.setup();
  const field = screen.getByLabelText("System prompt");
  await user.clear(field);
  await user.type(field, text);
  return user;
}

describe("InstructionsTab", () => {
  it("saves only the instructions; starters live in General settings", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    renderWithI18n(tab(baseAgent, onSave));

    expect(screen.queryByText("Conversation starters")).toBeNull();
    const user = await typeInstructions("Updated instructions.");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      instructions: "Updated instructions.",
    });
  });

  it("discards unsaved edits back to the saved instructions", async () => {
    renderWithI18n(tab(baseAgent));

    const user = await typeInstructions("Scratch that.");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Discard" }));

    expect(screen.getByLabelText("System prompt")).toHaveValue(
      "Review carefully.",
    );
    expect(screen.queryByText("Unsaved changes")).toBeNull();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("counts characters and previews an empty prompt without an editor", async () => {
    renderWithI18n(tab({ ...baseAgent, instructions: "" }));

    expect(screen.getByText("0 characters")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("tab", { name: "Preview" }));

    expect(screen.queryByLabelText("System prompt")).toBeNull();
    expect(screen.getByText("Nothing to preview yet.")).toBeInTheDocument();
  });

  it("preserves an unsaved edit when the server copy changes", async () => {
    const { rerender } = renderWithI18n(tab(baseAgent));
    await typeInstructions("Local draft.");

    rerender(tab({ ...baseAgent, instructions: "Server-side change." }));

    expect(screen.getByLabelText("System prompt")).toHaveValue("Local draft.");
  });

  it("adopts a server change while the editor is clean", () => {
    const { rerender } = renderWithI18n(tab(baseAgent));

    rerender(tab({ ...baseAgent, instructions: "Server-side change." }));

    expect(screen.getByLabelText("System prompt")).toHaveValue(
      "Server-side change.",
    );
  });

  it("keeps submitted edits when an optimistic update rolls back", async () => {
    let rejectSave!: (reason?: unknown) => void;
    const onSave = vi.fn(
      () =>
        new Promise<void>((_, reject) => {
          rejectSave = reject;
        }),
    );
    const { rerender } = renderWithI18n(tab(baseAgent, onSave));
    const user = await typeInstructions("Inspect the patch.");
    await user.click(screen.getByRole("button", { name: "Save" }));

    rerender(tab({ ...baseAgent, instructions: "Inspect the patch." }, onSave));
    rerender(tab(baseAgent, onSave));
    await act(async () => rejectSave(new Error("Update failed")));

    expect(screen.getByLabelText("System prompt")).toHaveValue(
      "Inspect the patch.",
    );
  });
});

// DENE-304: a specialisation's prompt has two halves, and the tab has to show
// which half is whose. The inherited half is read-only (it is edited on the
// base role, and the server never accepts an override), and the effective
// prompt preview is the composed string the daemon is actually handed.
describe("InstructionsTab inheritance", () => {
  const specializationAgent: Agent = {
    ...baseAgent,
    id: "agent-spec",
    name: "Nightly Reviewer",
    instructions: "Only review the diff.",
    parent_agent_id: "agent-base",
    parent_agent_name: "Base Reviewer",
    inherited_instructions: "Review carefully.",
  };

  it("shows the base role's prompt as a read-only block", () => {
    renderWithI18n(tab(specializationAgent));

    const block = screen.getByTestId("agent-inherited-prompt");
    expect(block).toHaveTextContent("Base role prompt");
    expect(block).toHaveTextContent(
      /edit it on "Base Reviewer" to update every specialization/i,
    );
    // The editable field is the ADDITIONAL half, not the whole prompt.
    expect(screen.getByLabelText(/Additional instructions/i)).toHaveValue(
      "Only review the diff.",
    );
  });

  it("previews the composed prompt the runtime will receive", async () => {
    const user = userEvent.setup();
    renderWithI18n(tab(specializationAgent));

    const preview = screen.getByTestId("agent-effective-prompt");
    await user.click(
      within(preview).getByRole("button", { name: /^Show$/i }),
    );

    // Compared as raw text: `toHaveTextContent` collapses whitespace, and the
    // blank line between the halves is the point.
    expect(screen.getByTestId("agent-effective-prompt-text").textContent).toBe(
      "Review carefully.\n\nOnly review the diff.",
    );
  });

  it("keeps the preview in step with an unsaved edit", async () => {
    const user = userEvent.setup();
    renderWithI18n(tab(specializationAgent));

    const editor = screen.getByLabelText(/Additional instructions/i);
    await user.clear(editor);
    await user.type(editor, "New rules");

    const preview = screen.getByTestId("agent-effective-prompt");
    await user.click(
      within(preview).getByRole("button", { name: /^Show$/i }),
    );

    // What is previewed is what would run — not the last saved value.
    expect(screen.getByTestId("agent-effective-prompt-text").textContent).toBe(
      "Review carefully.\n\nNew rules",
    );
  });

  it("does not invent a parent half when the base role's prompt is unavailable", () => {
    renderWithI18n(
      tab({
        ...specializationAgent,
        inherited_instructions: "",
      }),
    );

    expect(screen.getByTestId("agent-inherited-prompt")).toHaveTextContent(
      /has no prompt, or you don't have access to it/i,
    );
  });

  // DENE-384: an absent `inherited_instructions` used to be stated as a fact
  // about the base role even when the read that carries it had failed, which
  // read as a business conclusion and misled a real acceptance pass.
  it("reports a failed parent read as a read failure, with a retry", async () => {
    const user = userEvent.setup();
    const onRetryInheritedPrompt = vi.fn();
    renderWithI18n(
      tab(
        { ...specializationAgent, inherited_instructions: "" },
        vi.fn().mockResolvedValue(undefined),
        { inheritedPromptState: "failed", onRetryInheritedPrompt },
      ),
    );

    const block = screen.getByTestId("agent-inherited-prompt");
    expect(block).not.toHaveTextContent(
      /has no prompt, or you don't have access to it/i,
    );
    expect(block).toHaveTextContent(/couldn't load the base role's prompt/i);

    await user.click(screen.getByTestId("agent-inherited-prompt-retry"));
    expect(onRetryInheritedPrompt).toHaveBeenCalledTimes(1);
  });

  it("shows the composed preview only once the parent half is known", async () => {
    const user = userEvent.setup();
    renderWithI18n(
      tab(
        { ...specializationAgent, inherited_instructions: "" },
        vi.fn().mockResolvedValue(undefined),
        { inheritedPromptState: "loading" },
      ),
    );

    const block = screen.getByTestId("agent-inherited-prompt");
    expect(block).not.toHaveTextContent(
      /has no prompt, or you don't have access to it/i,
    );
    expect(block).toHaveTextContent(/loading the base role's prompt/i);

    // "Exactly what the runtime receives" would be a lie while the parent
    // half is still unknown — the composed text is withheld until it lands.
    const preview = screen.getByTestId("agent-effective-prompt");
    await user.click(within(preview).getByRole("button", { name: /^Show$/i }));
    expect(
      screen.queryByTestId("agent-effective-prompt-text"),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("agent-effective-prompt-unknown")).toHaveTextContent(
      /loading the base role's prompt/i,
    );
  });

  it("tells a base role which specialisations a prompt edit reaches", () => {
    renderWithI18n(
      <InstructionsTab
        agent={{ ...baseAgent, name: "Base Reviewer" }}
        onSave={vi.fn().mockResolvedValue(undefined)}
        childAgents={[
          { ...baseAgent, id: "agent-spec", name: "Nightly Reviewer" },
        ]}
      />,
    );

    const hint = screen.getByTestId("agent-specialization-children");
    expect(hint).toHaveTextContent(
      /also changes every specialization of "Base Reviewer"/i,
    );
    expect(hint).toHaveTextContent("Nightly Reviewer");
  });

  it("stays a plain prompt editor for a base role with no specialisations", () => {
    renderWithI18n(tab({ ...baseAgent, name: "Base Reviewer" }));

    expect(
      screen.queryByTestId("agent-inherited-prompt"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agent-effective-prompt"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("agent-specialization-children"),
    ).not.toBeInTheDocument();
  });
});
