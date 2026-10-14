// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

function runtime(metadata: Record<string, unknown>) {
  return {
    id: "runtime-1",
    name: "Claude (kun-mac)",
    provider: "claude",
    metadata,
  } as AgentRuntime;
}

describe("AgentDetailInspector", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows the magpie route the runtime reported, with nothing to edit", () => {
    renderWithI18n(
      <AgentDetailInspector
        runtime={runtime({
          magpie: {
            group: "office-claude",
            models: [
              { id: "claude/claude-opus-5-5", name: "Opus 5.5" },
              { id: "claude/claude-sonnet-5-5", name: "Sonnet 5.5" },
            ],
          },
        })}
      />,
    );

    expect(screen.getByText("CLI")).toBeInTheDocument();
    expect(screen.getByText("Model")).toBeInTheDocument();
    expect(screen.getByText("office-claude")).toBeInTheDocument();
    expect(screen.getByText("Opus 5.5")).toBeInTheDocument();
    expect(screen.getByText("Sonnet 5.5")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("spinbutton")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });

  // No magpie on the machine: the CLI picks its own model.
  it("says the CLI uses its own models when no route was reported", () => {
    renderWithI18n(<AgentDetailInspector runtime={runtime({})} />);

    expect(screen.getByText("Not on magpie")).toBeInTheDocument();
  });
});
