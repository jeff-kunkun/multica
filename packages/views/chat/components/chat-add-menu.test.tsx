// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import type { Project } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ChatAddMenu } from "./chat-add-menu";

// Set arithmetic (which entry a toggle adds or drops, and in what order) is
// covered once in packages/core/chat/project-context.test.ts. This suite owns
// the wiring: what the menu renders, and the complete set it hands back.

function project(id: string, title: string): Project {
  return {
    id,
    workspace_id: "ws-1",
    title,
    description: null,
    icon: "\u{1F4D8}",
    status: "planned",
    priority: "none",
    lead_type: null,
    lead_id: null,
    start_date: null,
    due_date: null,
    created_at: new Date(0).toISOString(),
    updated_at: new Date(0).toISOString(),
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
  } as Project;
}

const ALPHA = project("project-alpha", "Project Alpha");
const BETA = project("project-beta", "Project Beta");

async function openProjectSubmenu(props: Partial<React.ComponentProps<typeof ChatAddMenu>>) {
  const onProjectsChange = vi.fn();
  renderWithI18n(
    <ChatAddMenu
      projects={[ALPHA, BETA]}
      projectIds={[]}
      onProjectsChange={onProjectsChange}
      {...props}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  fireEvent.click(await screen.findByRole("menuitem", { name: /^Project context/ }));
  return onProjectsChange;
}

afterEach(cleanup);

describe("ChatAddMenu project context", () => {
  it("renders every project as a checkbox reflecting the attached set", async () => {
    await openProjectSubmenu({ projectIds: [ALPHA.id] });

    expect(
      await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }),
    ).toBeChecked();
    expect(
      screen.getByRole("menuitemcheckbox", { name: /Project Beta/ }),
    ).not.toBeChecked();
  });

  // The whole point of DENE-522: a second project is ADDED, not swapped in.
  it("hands back the whole set when a second project is checked", async () => {
    const onProjectsChange = await openProjectSubmenu({ projectIds: [ALPHA.id] });

    fireEvent.click(
      await screen.findByRole("menuitemcheckbox", { name: /Project Beta/ }),
    );

    expect(onProjectsChange).toHaveBeenCalledWith([ALPHA.id, BETA.id]);
  });

  it("drops just the unchecked project", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id, BETA.id],
    });

    fireEvent.click(
      await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }),
    );

    expect(onProjectsChange).toHaveBeenCalledWith([BETA.id]);
  });

  it("clears the whole set from the remove-all entry", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id, BETA.id],
    });

    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Remove all project context" }),
    );

    expect(onProjectsChange).toHaveBeenCalledWith([]);
  });

  it("offers no remove-all entry when nothing is attached", async () => {
    await openProjectSubmenu({ projectIds: [] });

    expect(await screen.findByRole("menuitemcheckbox", { name: /Project Alpha/ }))
      .toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "Remove all project context" }),
    ).toBeNull();
  });

  it("explains an empty project list instead of rendering an empty menu", async () => {
    await openProjectSubmenu({ projects: [] });

    expect(await screen.findByText("No projects yet")).toBeInTheDocument();
  });

  // Soft gate (MUL-5150): the daemon warning must inform, never lock the
  // selection — a user can still attach context for the run after an upgrade.
  it("warns about an outdated daemon without disabling selection", async () => {
    const onProjectsChange = await openProjectSubmenu({
      projectIds: [ALPHA.id],
      projectContextUnsupported: true,
    });

    expect(
      await screen.findByText(
        "Project description won't apply — this agent's daemon needs an upgrade",
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /Project Beta/ }));
    expect(onProjectsChange).toHaveBeenCalled();
  });
});
