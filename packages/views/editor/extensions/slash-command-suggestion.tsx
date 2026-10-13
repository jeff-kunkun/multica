"use client";

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import { Extension } from "@tiptap/core";
import { Suggestion, type SuggestionOptions } from "@tiptap/suggestion";
import { PluginKey } from "@tiptap/pm/state";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../../i18n";
import {
  createSuggestionPopupRender,
} from "./suggestion-popup";
import {
  isPickerAcceptKey,
  pickerNavigationDirection,
} from "../../common/picker-keys";
import { isTriggerArmedAt } from "./suggestion-trigger-arming";

const MAX_ITEMS = 20;

/** Known built-in command ids — the keys under editor `slash_command.commands`. */
export type BuiltinCommandKey = "note";

export interface SlashCommandItem {
  id: string;
  label: string;
  /**
   * The i18n key under editor `slash_command.commands`, so the visible
   * description stays localized (the typed `/label` does not).
   */
  descriptionKey: BuiltinCommandKey;
}

interface SlashCommandListProps {
  items: SlashCommandItem[];
  command: (item: SlashCommandItem) => void;
}

export interface SlashCommandListRef {
  onKeyDown: (props: { event: KeyboardEvent }) => boolean;
}

export const SlashCommandList = forwardRef<
  SlashCommandListRef,
  SlashCommandListProps
>(function SlashCommandList({ items, command }, ref) {
  const { t } = useT("editor");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);

  useEffect(() => {
    setSelectedIndex(0);
  }, [items]);

  useEffect(() => {
    itemRefs.current[selectedIndex]?.scrollIntoView({ block: "nearest" });
  }, [selectedIndex]);

  const selectItem = useCallback(
    (index: number) => {
      const item = items[index];
      if (!item) return;
      command(item);
    },
    [items, command],
  );

  useImperativeHandle(ref, () => ({
    onKeyDown: ({ event }) => {
      if (isImeComposing(event)) return false;
      // Arrow keys plus the Ctrl+N/J/P/K aliases the command bar accepts —
      // see pickerNavigationDirection.
      const direction = pickerNavigationDirection(event);
      if (direction !== null) {
        if (items.length === 0) return false;
        const delta = direction === "next" ? 1 : items.length - 1;
        setSelectedIndex((i) => (i + delta) % items.length);
        return true;
      }
      // Enter is the canonical accept; plain Tab is an additive alias (see
      // isPickerAcceptKey). Shift/modifier+Tab fall through to focus nav.
      if (isPickerAcceptKey(event)) {
        if (items.length === 0) return false;
        selectItem(selectedIndex);
        return true;
      }
      return false;
    },
  }));

  // `/` is common in prose (paths, dates), so an empty match renders nothing
  // rather than an empty-state box on every slash.
  if (items.length === 0) return null;

  const describe = (item: SlashCommandItem): string =>
    t(($) => $.slash_command.commands[item.descriptionKey]);

  return (
    // Height budget clamps to min(design max, viewport-aware
    // `--suggestion-available-height` from suggestion-popup.tsx's size
    // middleware), falling back to the design max when rendered standalone.
    // Single height authority — mirrors MentionList.
    <div className="rounded-md border bg-popover py-1 shadow-md w-72 max-h-[min(300px,var(--suggestion-available-height,300px))] overflow-y-auto">
      {items.map((item, index) => {
        return (
          <button
            key={item.id}
            ref={(el) => {
              itemRefs.current[index] = el;
            }}
            className={`flex w-full flex-col gap-0.5 px-3 py-1.5 text-left text-caption transition-colors ${
              selectedIndex === index ? "bg-accent" : "hover:bg-accent/50"
            }`}
            onClick={() => selectItem(index)}
          >
            <span className="font-medium">/{item.label}</span>
            <span className="truncate text-muted-foreground">
              {describe(item)}
            </span>
          </button>
        );
      })}
    </div>
  );
});

// ---------------------------------------------------------------------------
// Built-in command menu (issue comments)
// ---------------------------------------------------------------------------

/**
 * Built-in slash commands offered in the issue comment composer: a fixed,
 * hand-curated set. Currently only `/note`, which marks a comment as a
 * human-only note that won't trigger the assigned agent — mirrors the backend
 * `noteCommentPrefix` in server/internal/handler/comment.go.
 */
export const BUILTIN_COMMANDS: SlashCommandItem[] = [
  { id: "note", label: "note", descriptionKey: "note" },
];

// Match on the command label as a prefix only — the description is for display,
// not search. With a single command this keeps the menu predictable (typing
// `/no` surfaces `note`; an unrelated `/deploy` shows nothing).
export function buildBuiltinCommandItems(query: string): SlashCommandItem[] {
  const q = query.toLowerCase();
  return BUILTIN_COMMANDS
    .filter((c) => c.label.toLowerCase().startsWith(q))
    .slice(0, MAX_ITEMS);
}

export function createBuiltinCommandSuggestion(): Omit<SuggestionOptions<SlashCommandItem>, "editor"> {
  const pluginKey = new PluginKey("builtinCommandSuggestion");

  return {
    char: "/",
    pluginKey,
    // Only open over a `/` the user actually typed, so a pasted path
    // (`/usr/local/bin`) never opens the command menu (MUL-5429).
    shouldShow: ({ editor, range }) => isTriggerArmedAt(editor, range.from),
    items: ({ query }) => buildBuiltinCommandItems(query),
    command: ({ editor, range, props }) => {
      // Insert the plain-text prefix (e.g. "/note ") rather than a rich node,
      // so a menu selection and a hand-typed command are byte-identical and the
      // backend can detect the marker with a simple prefix match. The trailing
      // space terminates the suggestion match so the menu does not re-open.
      editor
        .chain()
        .focus()
        .insertContentAt(range, [{ type: "text", text: `/${props.label} ` }])
        .run();

      window.getSelection()?.collapseToEnd();
    },
    render: createSuggestionPopupRender<SlashCommandItem, SlashCommandItem, SlashCommandListRef, SlashCommandListProps>({
      pluginKey,
      component: SlashCommandList,
      getProps: (props) => ({
        items: props.items,
        command: props.command,
      }),
      onKeyDown: (ref, props) => ref?.onKeyDown(props) ?? false,
    }),
  };
}

/** Hosts the built-in command suggestion plugin in editors that enable it. */
export const BuiltinCommandExtension = Extension.create({
  name: "builtinCommand",

  addProseMirrorPlugins() {
    return [
      Suggestion<SlashCommandItem>({
        editor: this.editor,
        ...createBuiltinCommandSuggestion(),
      }),
    ];
  },
});
