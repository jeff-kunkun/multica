"use client";

import { useCallback, useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import type { IssueDraftPayload, IssuePriority, IssueStatus, MemberWithUser, RuntimeDevice } from "@multica/core/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { RuntimePicker } from "../../agents/components/runtime-picker";
import { PriorityPicker } from "../components/pickers/priority-picker";
import { StatusPicker } from "../components/pickers/status-picker";
import { useT } from "../../i18n";

/**
 * The right-hand column: exactly what pressing "confirm and create" will write.
 *
 * The four fields the server reads at finalize are the four shown here, and
 * they are editable — a conversation is a good way to arrive at a draft and a
 * bad way to fix a typo in it. Everything is local until "save", so the preview
 * never races the carrier's own revisions into the server one keystroke at a
 * time.
 */
export function IssueDraftPreviewPanel({
  draft,
  stage,
  canConfirm,
  saving,
  saved,
  confirming,
  abandoning,
  runtime,
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  switchingRuntime,
  pending,
  onDirtyChange,
  onSave,
  onGenerate,
  onConfirm,
  onAbandon,
  onSwitchRuntime,
}: {
  draft: IssueDraftPayload | null;
  stage: "aligning" | "ready" | "creating" | "created";
  canConfirm: boolean;
  saving: boolean;
  saved: boolean;
  confirming: boolean;
  abandoning: boolean;
  runtime: RuntimeDevice | null;
  runtimes: RuntimeDevice[];
  runtimesLoading: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  switchingRuntime: boolean;
  /** A turn is running: nothing may be written while the carrier is replying. */
  pending: boolean;
  /**
   * Reports whether the editor holds unsaved edits. The alignment session uses
   * it to decide whether it may adopt a carrier revision on its own: an edit in
   * progress is the user's, and nothing may be written over it.
   */
  onDirtyChange: (dirty: boolean) => void;
  onSave: (draft: IssueDraftPayload, status?: "draft" | "ready") => Promise<boolean>;
  /** Folds the carrier's latest draft block into what is on screen and marks it
   *  ready — the step that turns "we agreed" into something confirmable.
   *  Resolves with the draft the server now holds, or null when nothing was
   *  written; the editor adopts it. */
  onGenerate: (draft: IssueDraftPayload) => Promise<IssueDraftPayload | null>;
  onConfirm: () => Promise<boolean>;
  onAbandon: () => Promise<boolean>;
  onSwitchRuntime: (runtimeId: string) => Promise<string | null>;
}) {
  const { t } = useT("issues");
  const [editing, setEditing] = useState<IssueDraftPayload | null>(draft);
  const [confirmingAbandon, setConfirmingAbandon] = useState(false);

  // The server revision the editor was last synchronised with. `dirty` is "the
  // user has typed something since then", NOT "the editor differs from the
  // server": the server moves on its own, because every carrier reply is folded
  // into the draft as it arrives. Comparing against the live server value made
  // that ordinary movement look like an unsaved edit — the panel froze on the
  // seed values it mounted with, reported `dirty` forever, and that in turn
  // blocked the fold from ever updating it again (DENE-319).
  const [baseline, setBaseline] = useState<IssueDraftPayload | null>(draft);
  const dirty =
    editing !== null && baseline !== null && !sameDraft(editing, baseline);

  /** Take the server's revision as both what is shown and what "clean" means. */
  const adopt = useCallback((next: IssueDraftPayload | null) => {
    setEditing(next);
    setBaseline(next);
  }, []);

  // A new server revision replaces the local copy only when the user has
  // nothing unsaved, which is what keeps a carrier reply from wiping an edit in
  // progress.
  useEffect(() => {
    if (!dirty) adopt(draft);
  }, [adopt, draft, dirty]);
  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);

  const value = editing ?? draft ?? EMPTY_DRAFT;
  const locked = pending || confirming || stage === "created";
  const canSave = !locked && !saving && value.title.trim().length > 0;
  // A title is not required to generate: the carrier's block is the only place
  // a title comes from before someone types one, so gating this button on the
  // title deadlocks the one step that can supply it. "Nothing to fold in yet"
  // is answered by the generate call itself, which writes nothing.
  const canGenerate = !locked && !saving;
  // Whether the server still has a draft to write. Once it is gone — the row
  // was confirmed or retired — the panel is a read-only leftover, and anything
  // it writes is a request against a draft that no longer exists.
  const hasDraft = draft !== null;
  // Which lifecycle state an explicit save writes back. `ready` is preserved:
  // this button refines the words of a draft the user already converged on, and
  // the server reads an omitted status as `draft` — so saving a tweak to a
  // generated preview would otherwise close the confirm gate again. The fold
  // path has held this line since DENE-279 (`planIssueDraftFold` never
  // downgrades `ready`).
  const saveStatus = stage === "ready" ? "ready" : "draft";

  const handleGenerate = () => {
    void onGenerate(value).then((persisted) => {
      // Adopt what was just persisted. Leaving the pre-generate value on screen
      // keeps `dirty` true forever, and the next "save draft" then overwrites
      // the generated preview with it — the carrier's work, silently lost.
      if (persisted) adopt(persisted);
    });
  };

  const handleSave = () => {
    void onSave(value, saveStatus).then((savedNow) => {
      // What the server now holds is the new clean point; the server's own echo
      // arrives as a `draft` prop and is adopted from there.
      if (savedNow) setBaseline(value);
    });
  };

  const handleSelectRuntime = useCallback(
    (runtimeId: string) => {
      if (!runtimeId || runtimeId === runtime?.id) return;
      // The picker seeds an empty selection by itself, and this callback is
      // what that seed lands on. With no draft on the server there is nothing
      // to rebind, and writing anyway is a request per render against a draft
      // the server has already retired.
      if (locked || !hasDraft) return;
      void onSwitchRuntime(runtimeId);
    },
    [hasDraft, locked, onSwitchRuntime, runtime?.id],
  );

  return (
    <div className="flex h-full min-h-0 flex-col border-l bg-muted/10">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-2xl px-5 py-6">
          <div className="mb-5">
            <h2 className="text-title-sm font-semibold tracking-tight">
              {t(($) => $.alignment.preview_title)}
            </h2>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.alignment.preview_hint)}
            </p>
          </div>

          <div className="space-y-5">
            <Field label={t(($) => $.alignment.field_title)} htmlFor="issue-draft-title">
              <Input
                id="issue-draft-title"
                value={value.title}
                disabled={locked}
                placeholder={t(($) => $.alignment.field_title_placeholder)}
                onChange={(event) =>
                  setEditing({ ...value, title: event.target.value })
                }
              />
            </Field>

            <Field
              label={t(($) => $.alignment.field_description)}
              htmlFor="issue-draft-description"
            >
              <Textarea
                id="issue-draft-description"
                value={value.description}
                disabled={locked}
                rows={10}
                placeholder={t(($) => $.alignment.field_description_placeholder)}
                onChange={(event) =>
                  setEditing({ ...value, description: event.target.value })
                }
              />
            </Field>

            <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
              <div className="flex items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.field_status)}
                </span>
                <StatusPicker
                  status={(value.status || null) as IssueStatus | null}
                  onUpdate={(updates) =>
                    setEditing({ ...value, status: updates.status ?? "" })
                  }
                />
              </div>
              <div className="flex items-center gap-2">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.alignment.field_priority)}
                </span>
                <PriorityPicker
                  priority={(value.priority || null) as IssuePriority | null}
                  onUpdate={(updates) =>
                    setEditing({ ...value, priority: updates.priority ?? "" })
                  }
                />
              </div>
            </div>

            <div className="space-y-2">
              <span className="text-caption text-muted-foreground">
                {t(($) => $.alignment.entry_runtime)}
              </span>
              <RuntimePicker
                runtimes={runtimes}
                runtimesLoading={runtimesLoading}
                members={members}
                currentUserId={currentUserId}
                selectedRuntimeId={runtime?.id ?? ""}
                // The rebind has to commit server-side before the picker moves:
                // showing runtime B while messages still run on A is exactly
                // what MUL-5163 fixed in the agent builder.
                onSelect={handleSelectRuntime}
                disabled={locked || switchingRuntime || pending}
              />
            </div>
          </div>
        </div>
      </div>

      <footer className="shrink-0 border-t bg-background px-5 py-3">
        {stage === "created" ? (
          <p className="text-body font-medium text-foreground">
            {t(($) => $.alignment.created_title)}
          </p>
        ) : (
          <>
            <p className="mb-3 text-caption text-muted-foreground">
              {t(($) => $.alignment.confirm_hint)}
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                onClick={() => void onConfirm()}
                disabled={!canConfirm}
                className="min-w-32"
              >
                {confirming ? (
                  <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                ) : null}
                {confirming
                  ? t(($) => $.alignment.confirming)
                  : t(($) => $.alignment.confirm)}
              </Button>
              <Button
                variant="outline"
                onClick={handleGenerate}
                // Available while the draft is still editable, `ready`
                // included: a converged draft that the user keeps refining
                // produces new carrier blocks, and refusing to fold them in
                // would leave retyping as the only way to apply them.
                disabled={!canGenerate}
              >
                {t(($) => $.alignment.generate)}
              </Button>
              <Button
                variant="outline"
                onClick={handleSave}
                disabled={!canSave}
              >
                {saving
                  ? t(($) => $.alignment.saving)
                  : saved && !dirty
                    ? t(($) => $.alignment.saved)
                    : t(($) => $.alignment.save)}
              </Button>
              <Button
                variant="ghost"
                className={cn("ml-auto text-muted-foreground")}
                onClick={() => setConfirmingAbandon(true)}
                disabled={locked || abandoning}
              >
                {abandoning
                  ? t(($) => $.alignment.abandoning)
                  : t(($) => $.alignment.abandon)}
              </Button>
            </div>
          </>
        )}
      </footer>

      <AlertDialog open={confirmingAbandon} onOpenChange={setConfirmingAbandon}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.alignment.abandon_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.alignment.abandon_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={abandoning}>
              {t(($) => $.alignment.abandon_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={abandoning}
              onClick={(event) => {
                // Keep the dialog up until the server has actually accepted;
                // `onAbandon` navigates away on success.
                event.preventDefault();
                void onAbandon();
              }}
            >
              {t(($) => $.alignment.confirm_abandon)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-2">
      <label htmlFor={htmlFor} className="text-caption text-muted-foreground">
        {label}
      </label>
      {children}
    </div>
  );
}

const EMPTY_DRAFT: IssueDraftPayload = {
  title: "",
  description: "",
  status: "",
  priority: "",
};

function sameDraft(a: IssueDraftPayload, b: IssueDraftPayload): boolean {
  return (
    a.title === b.title &&
    a.description === b.description &&
    a.status === b.status &&
    a.priority === b.priority
  );
}
