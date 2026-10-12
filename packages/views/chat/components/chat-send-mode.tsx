"use client";

import { useState, type SyntheticEvent } from "react";
import { ChevronDown, Loader2 } from "lucide-react";
import type { ChatSendMode } from "@multica/core/types";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Sheet,
  SheetContent,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

const MODES: ChatSendMode[] = ["steer", "queue", "restart", "replace"];

/** The mode Enter uses while a reply is running: steer when the CLI can take it. */
export function defaultChatSendMode(steerSupported: boolean | undefined): ChatSendMode {
  return steerSupported ? "steer" : "queue";
}

/** A chosen mode that is no longer available falls back to the default. */
export function effectiveChatSendMode(
  chosen: ChatSendMode | null,
  steerSupported: boolean | undefined,
  replaceAllowed = true,
): ChatSendMode {
  if (chosen === "steer" && !steerSupported) return "queue";
  if (chosen === "replace" && !replaceAllowed) return defaultChatSendMode(steerSupported);
  return chosen ?? defaultChatSendMode(steerSupported);
}

function cliName(provider: string): string {
  return provider ? provider.charAt(0).toUpperCase() + provider.slice(1) : provider;
}

/** Keep the composer focused so tapping the main button does not drop the keyboard. */
function keepFocusInComposer(event: SyntheticEvent<HTMLButtonElement>) {
  event.preventDefault();
}

interface ChatSendModeButtonProps {
  mode: ChatSendMode;
  automatic?: boolean;
  steerSupported?: boolean;
  steerProvider?: string;
  /** "restart" when steering stops the CLI and resumes its session. */
  steerMode?: string;
  /** Why replace is unavailable here (DENE-1934); unset when it is allowed. */
  replaceUnavailable?: string;
  /** False while the draft is empty or a send is in flight. */
  canSend: boolean;
  loading?: boolean;
  onModeChange: (mode: ChatSendMode) => void;
  onSend: (mode?: ChatSendMode) => void;
}

/**
 * Split send button shown while a reply is running (DENE-1346): the main half
 * sends with the current mode, ▾ picks another. Picking a row with a draft
 * sends it right away; with an empty draft it only sets the mode. Desktop gets
 * a menu, phones a bottom sheet with 44px rows.
 */
export function ChatSendModeButton({
  mode,
  automatic = false,
  steerSupported,
  steerProvider,
  steerMode,
  replaceUnavailable,
  canSend,
  loading,
  onModeChange,
  onSend,
}: ChatSendModeButtonProps) {
  const { t } = useT("chat");
  const isMobile = useIsMobile();
  const [sheetOpen, setSheetOpen] = useState(false);

  const label = (m: ChatSendMode) =>
    m === "steer"
      ? t(($) => $.input.mode_steer)
      : m === "queue"
        ? t(($) => $.input.mode_queue)
        : m === "replace"
          ? t(($) => $.input.mode_replace)
          : t(($) => $.input.mode_restart);
  const description = (m: ChatSendMode) =>
    m === "steer"
      ? steerMode === "restart"
        ? t(($) => $.input.mode_steer_restart_desc)
        : steerMode === "handoff"
          ? t(($) => $.input.mode_steer_desc_handoff)
          : t(($) => $.input.mode_steer_desc)
      : m === "queue"
        ? t(($) => $.input.mode_queue_desc)
        : m === "replace"
          ? t(($) => $.input.mode_replace_desc)
          : t(($) => $.input.mode_restart_desc);
  const processLine = (m: ChatSendMode) =>
    m === "steer"
      ? steerMode === "restart"
        ? t(($) => $.input.mode_steer_restart_process)
        : steerMode === "handoff"
          ? t(($) => $.input.mode_steer_process_handoff)
          : t(($) => $.input.mode_steer_process)
      : m === "queue"
        ? t(($) => $.input.mode_queue_process)
        : m === "replace"
          ? t(($) => $.input.mode_replace_process)
          : t(($) => $.input.mode_restart_process);
  const steerReason = steerProvider
    ? t(($) => $.input.mode_steer_unsupported, { cli: cliName(steerProvider) })
    : t(($) => $.input.mode_steer_unsupported_generic);

  // The reason a row cannot be picked, or null when it can.
  const unavailable = (m: ChatSendMode) =>
    m === "steer" && !steerSupported ? steerReason : m === "replace" ? (replaceUnavailable ?? null) : null;

  const pick = (next: ChatSendMode) => {
    if (unavailable(next)) return;
    onModeChange(next);
    setSheetOpen(false);
    if (canSend) onSend(next);
  };

  const rowText = (m: ChatSendMode) => {
    const reason = unavailable(m);
    const disabled = !!reason;
    return (
      <span className="flex min-w-0 flex-col gap-0.5 text-left">
        <span>{label(m)}</span>
        <span className="text-caption text-muted-foreground">
          {reason ?? description(m)}
        </span>
        {!disabled && (
          <span className="text-caption text-muted-foreground">{processLine(m)}</span>
        )}
      </span>
    );
  };

  const trigger = (
    <Button
      type="button"
      size="icon-sm"
      variant="default"
      className="w-6 rounded-l-none rounded-r-full border-l border-primary-foreground/20"
      aria-label={t(($) => $.input.mode_menu)}
      // On phones the tap would blur the editor and shift the composer
      // before the click lands, so the sheet never opened.
      onPointerDown={isMobile ? keepFocusInComposer : undefined}
      onMouseDown={isMobile ? keepFocusInComposer : undefined}
      onClick={isMobile ? () => setSheetOpen(true) : undefined}
    >
      <ChevronDown aria-hidden="true" />
    </Button>
  );

  return (
    <div className="flex items-center" data-slot="chat-send-mode">
      <Button
        type="button"
        size="sm"
        className="h-7 rounded-l-full rounded-r-none pl-3 pr-2"
        disabled={!canSend || loading}
        aria-busy={loading || undefined}
        onPointerDown={keepFocusInComposer}
        onMouseDown={keepFocusInComposer}
        onClick={() => automatic ? onSend() : onSend(mode)}
      >
        {loading ? <Loader2 className="animate-spin" aria-hidden="true" /> : automatic ? t(($) => $.input.send_tooltip) : label(mode)}
      </Button>
      {isMobile ? (
        <>
          {trigger}
          <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
            <SheetContent side="bottom" showCloseButton={false} className="gap-0 px-2 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
              <SheetTitle className="sr-only">{t(($) => $.input.mode_menu)}</SheetTitle>
              {MODES.map((m) => {
                const disabled = !!unavailable(m);
                return (
                  <button
                    key={m}
                    type="button"
                    disabled={disabled}
                    aria-pressed={!automatic && m === mode}
                    onClick={() => pick(m)}
                    className={cn(
                      "flex min-h-11 w-full items-center rounded-md px-3 py-2 text-body",
                      !automatic && m === mode && "bg-accent",
                      disabled && "opacity-60",
                    )}
                  >
                    {rowText(m)}
                  </button>
                );
              })}
            </SheetContent>
          </Sheet>
        </>
      ) : (
        <DropdownMenu>
          <DropdownMenuTrigger render={trigger} />
          <DropdownMenuContent side="top" align="end" className="w-72">
            <DropdownMenuRadioGroup
              value={automatic ? "" : mode}
              onValueChange={(value) => pick(value as ChatSendMode)}
            >
              {MODES.map((m) => (
                <DropdownMenuRadioItem
                  key={m}
                  value={m}
                  disabled={!!unavailable(m)}
                  className="items-start py-1.5"
                >
                  {rowText(m)}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

/**
 * Confirms a replace send (DENE-1934): it stops the running reply and cancels
 * every queued message, so the count is stated before anything is sent.
 */
export function ChatReplaceConfirmDialog({
  open,
  queuedCount,
  onSettle,
}: {
  open: boolean;
  queuedCount: number;
  onSettle: (confirmed: boolean) => void;
}) {
  const { t } = useT("chat");
  return (
    <AlertDialog open={open} onOpenChange={(next) => { if (!next) onSettle(false); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.input.replace_confirm_title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {queuedCount > 0
              ? t(($) => $.input.replace_confirm_desc, { count: queuedCount })
              : t(($) => $.input.replace_confirm_desc_none)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel className="min-h-11">{t(($) => $.input.replace_confirm_keep)}</AlertDialogCancel>
          <AlertDialogAction
            className="min-h-11"
            variant="destructive"
            onClick={(event) => {
              event.preventDefault();
              onSettle(true);
            }}
          >
            {t(($) => $.input.replace_confirm_action)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
