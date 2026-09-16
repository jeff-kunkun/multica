// @vitest-environment jsdom

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { IssueDraftPayload, IssuePriority, IssueStatus } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { IssueDraftPreviewPanel } from "./issue-draft-preview-panel";

/**
 * The preview panel is the write side of an alignment: what it shows is what
 * "confirm and create" will hand the server, and what it holds when someone
 * presses save is what the server will store. These pin the two ways that
 * contract broke in the DENE-317 acceptance run — a generate that never came
 * back to the editor, and a picker that kept writing to a draft the server had
 * already retired.
 */

const mocks = vi.hoisted(() => ({
  // What the real picker's empty-selection seed does: it calls `onSelect`
  // whenever it is asked to show no runtime. The alignment page's selection is
  // server state, so that is the whole window after a confirm.
  seedRuntimeId: "rt-2" as string,
}));

vi.mock("../../agents/components/runtime-picker", () => ({
  RuntimePicker: ({ onSelect }: { onSelect: (id: string) => void }) => {
    // No dependency array: this is the shape that made the real picker seed on
    // every render of a parent whose callback identity changed.
    useEffect(() => {
      if (mocks.seedRuntimeId) onSelect(mocks.seedRuntimeId);
    });
    return <div data-testid="runtime-picker" />;
  },
}));

vi.mock("../components/pickers/status-picker", () => ({
  StatusPicker: ({ onUpdate }: { onUpdate: (u: { status: IssueStatus | null }) => void }) => (
    <button type="button" onClick={() => onUpdate({ status: "todo" })}>
      status-picker
    </button>
  ),
}));

vi.mock("../components/pickers/priority-picker", () => ({
  PriorityPicker: ({
    onUpdate,
  }: {
    onUpdate: (u: { priority: IssuePriority | null }) => void;
  }) => (
    <button type="button" onClick={() => onUpdate({ priority: "high" })}>
      priority-picker
    </button>
  ),
}));

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const STORED: IssueDraftPayload = {
  title: "",
  description: "收件箱只能逐条标记已读",
  status: "",
  priority: "",
};

/** What the carrier's block turns that into, once folded in. */
const MERGED: IssueDraftPayload = {
  title: "收件箱支持批量标记已读",
  description: "## 问题\n\n收件箱目前只能逐条标记已读。",
  status: "",
  priority: "",
};

type PanelProps = React.ComponentProps<typeof IssueDraftPreviewPanel>;

function renderPanel(overrides: Partial<PanelProps> = {}) {
  const props: PanelProps = {
    draft: STORED,
    stage: "aligning",
    canConfirm: false,
    saving: false,
    saved: false,
    confirming: false,
    abandoning: false,
    runtime: null,
    runtimes: [],
    runtimesLoading: false,
    members: [],
    currentUserId: null,
    switchingRuntime: false,
    pending: false,
    onDirtyChange: vi.fn(),
    onSave: vi.fn().mockResolvedValue(true),
    onGenerate: vi.fn().mockResolvedValue(MERGED),
    onConfirm: vi.fn().mockResolvedValue(true),
    onAbandon: vi.fn().mockResolvedValue(true),
    onSwitchRuntime: vi.fn().mockResolvedValue("rt-2"),
    ...overrides,
  };
  const element = (next: PanelProps) => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <IssueDraftPreviewPanel {...next} />
    </I18nProvider>
  );
  const utils = render(element(props));
  return {
    ...utils,
    props,
    /** Re-render with a changed prop, as the page does when the server answers. */
    rerenderWith: (next: Partial<PanelProps>) =>
      utils.rerender(element({ ...props, ...next })),
  };
}

function titleInput(): HTMLInputElement {
  return screen.getByLabelText("Title") as HTMLInputElement;
}

function descriptionInput(): HTMLTextAreaElement {
  return screen.getByLabelText("Description") as HTMLTextAreaElement;
}

beforeEach(() => {
  mocks.seedRuntimeId = "rt-2";
});
afterEach(() => cleanup());

describe("IssueDraftPreviewPanel generate", () => {
  it("shows the carrier's folded draft instead of the values it replaced", async () => {
    const { props, rerenderWith } = renderPanel();
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });

    fireEvent.click(screen.getByRole("button", { name: "Generate preview" }));
    await waitFor(() => expect(props.onGenerate).toHaveBeenCalledTimes(1));
    // The user's own edit is part of what they asked to have folded in.
    expect(props.onGenerate).toHaveBeenCalledWith({
      ...STORED,
      title: "手打的标题",
    });

    // The save response is what the page re-renders with.
    rerenderWith({ draft: MERGED });
    await waitFor(() => expect(titleInput().value).toBe(MERGED.title));
    expect(descriptionInput().value).toBe(MERGED.description);
  });

  it("saves what was generated, not the pre-generate values", async () => {
    // The data-loss shape from the acceptance run: the panel kept showing the
    // old title and the seed description, so the next save wrote them straight
    // back over the freshly generated draft and dropped it out of `ready`.
    const onSave = vi.fn().mockResolvedValue(true);
    const { props, rerenderWith } = renderPanel({ onSave });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate preview" }));
    await waitFor(() => expect(titleInput().value).toBe(MERGED.title));

    // The save response is what the page re-renders with; the editor must be
    // clean against it, not still holding the pre-generate values.
    rerenderWith({ draft: MERGED });
    await waitFor(() => expect(props.onDirtyChange).toHaveBeenLastCalledWith(false));

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith(MERGED, "draft");
  });

  it("keeps a ready draft ready when the edited preview is saved", async () => {
    // The save button refines the words; it is not a way to un-converge. The
    // server reads an omitted status as `draft`, so a save over a generated
    // preview used to close the confirm gate again (DENE-319).
    const onSave = vi.fn().mockResolvedValue(true);
    renderPanel({ stage: "ready", draft: { ...STORED, title: "T" }, onSave });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave.mock.calls[0]?.[1]).toBe("ready");
  });

  it("keeps the edits when the generate wrote nothing", async () => {
    // No title anywhere yet is the one case generate cannot fix; the editor
    // must hold on to what the user typed rather than clear itself.
    const { props } = renderPanel({
      onGenerate: vi.fn().mockResolvedValue(null),
    });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate preview" }));
    await waitFor(() => expect(props.onGenerate).toHaveBeenCalledTimes(1));
    expect(titleInput().value).toBe("手打的标题");
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(true);
  });
  it("offers generate before there is a title to save", async () => {
    // The carrier's block is the only place a title comes from before someone
    // types one, so a generate gated on the title deadlocks the step that
    // supplies it.
    renderPanel();
    expect(screen.getByRole("button", { name: "Generate preview" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
  });
});

describe("IssueDraftPreviewPanel server revisions", () => {
  it("adopts a reply the carrier folded in while the editor was untouched", () => {
    // The acceptance run's starting state: the panel mounted on the seed draft
    // (no title, the user's own words), the carrier's proposal was folded into
    // the server draft behind it, and the panel stayed on the seed — reporting
    // an unsaved edit the user never made, which is what blocked every later
    // fold and let the next save overwrite the generated draft.
    const { props, rerenderWith } = renderPanel();
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(false);

    rerenderWith({ draft: MERGED });
    expect(titleInput().value).toBe(MERGED.title);
    expect(descriptionInput().value).toBe(MERGED.description);
    expect(props.onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it("keeps a real edit when a server revision lands under it", () => {
    const onDirtyChange = vi.fn();
    const { rerenderWith } = renderPanel({ onDirtyChange });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    rerenderWith({ draft: MERGED });
    expect(titleInput().value).toBe("手打的标题");
    expect(descriptionInput().value).toBe(STORED.description);
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it("is clean again once the server has stored what was on screen", async () => {
    const onSave = vi.fn().mockResolvedValue(true);
    const { props, rerenderWith } = renderPanel({ onSave });
    fireEvent.change(titleInput(), { target: { value: "手打的标题" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));

    rerenderWith({ draft: { ...STORED, title: "手打的标题" } });
    await waitFor(() => expect(props.onDirtyChange).toHaveBeenLastCalledWith(false));
  });
});

describe("IssueDraftPreviewPanel runtime seeding", () => {
  it("does not write a seeded runtime to a draft the server no longer has", () => {
    // `draft === null` is the window after a confirm: the row is gone from the
    // unfinished list while the page is still on screen. The picker still seeds
    // an empty selection, and that seed must not become a rebind request per
    // render against a retired draft.
    const { props } = renderPanel({ draft: null, stage: "creating" });
    expect(screen.getByTestId("runtime-picker")).toBeTruthy();
    expect(props.onSwitchRuntime).not.toHaveBeenCalled();
  });

  it("still applies a picker selection while the draft is live", () => {
    const { props } = renderPanel({ runtime: null });
    expect(props.onSwitchRuntime).toHaveBeenCalledWith("rt-2");
  });

  it("ignores the seed once the draft is created", () => {
    const { props } = renderPanel({ stage: "created" });
    expect(props.onSwitchRuntime).not.toHaveBeenCalled();
  });
});
