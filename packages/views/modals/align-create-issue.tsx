"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftRight, Loader2, Sparkles } from "lucide-react";
import { clientErrorMessage } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  issueDraftListOptions,
  markIssueDraftSeedFailure,
  unfinishedIssueDrafts,
  useStartIssueDraft,
} from "@multica/core/issue-drafts";
import { useIssueDraftStore } from "@multica/core/issues/stores";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  isRuntimeUsableForUser,
  runtimeDisplayName,
  runtimeListOptions,
} from "@multica/core/runtimes";
import { contentReferencesAttachment, type IssueDraftSummary } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { DialogTitle } from "@multica/ui/components/ui/dialog";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { cn } from "@multica/ui/lib/utils";
import {
  ContentEditor,
  FileDropOverlay,
  useFileDropZone,
  useUploadGate,
  type ContentEditorRef,
} from "../editor";
import { useT } from "../i18n";
import { UnfinishedIssueDraftsBanner } from "../issues/draft/unfinished-issue-drafts";
import { ClearablePillButton } from "../common/pill-button";
import { ProjectPicker } from "../projects/components/project-picker";
import { AppLink, useNavigation } from "../navigation";
import { useIssueCreateUploads } from "./use-issue-create-uploads";

/**
 * The alignment face of the create-issue dialog (DENE-370) — the third mode of
 * the same shell, not a second dialog.
 *
 * Its job is unchanged from the standalone entry dialog it replaces: open the
 * conversation and hand off. It awaits the server's session id, sends the
 * user's own first turn, and navigates to the alignment page. The conversation
 * itself is deliberately NOT here: it is a durable object that is left and
 * resumed, and a modal cannot be refreshed into, linked to, or restored as a
 * desktop tab.
 *
 * What IS shared with "New issue" is everything about the input: the same
 * `ContentEditor`, the same upload pool (`draft.shared.attachments`), the same
 * optional project (`draft.shared.projectId`), and the same draft store, so a
 * file, a body or a project chosen on either face survives a switch to the
 * other. Which machine runs the alignment is still decided FOR the user
 * — the page's preview panel is where that choice is visible and changeable
 * afterwards. The single case that stops the conversation from starting at all
 * — nothing usable to run on, or the chosen machine offline — is stated
 * outright instead of being left for the user to infer from a disabled button.
 */
export function AlignCreatePanel({
  onClose,
  onSwitchMode,
}: {
  onClose: () => void;
  /** Called with the carry payload for the panel this face switches back to. */
  onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
}) {
  const { t } = useT("issues");
  const { t: tModals } = useT("modals");
  const { t: tProjects } = useT("projects");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);

  const draft = useIssueDraftStore((s) => s.draft);
  const setAlign = useIssueDraftStore((s) => s.setAlign);
  const setShared = useIssueDraftStore((s) => s.setShared);
  const setActiveMode = useIssueDraftStore((s) => s.setActiveMode);

  // The project is the SHARED slot's, exactly as it is on the manual face: it
  // is a property of the issue, not of the form that described it, so a project
  // picked here (or there) is what the whole group is filed under. Optional by
  // design — no project is a legitimate choice and the picker says so.
  const projectId = draft.shared.projectId;

  // The alignment request lives in the draft's own `align` slot, exactly like
  // the agent prompt: the manual face assist-inits it when it switches here,
  // and a switch away and back restores it verbatim. Nothing about the body
  // rides the carry channel — that is left to the parent-issue context, which
  // is not persisted at all.
  const initialRequest = draft.align.request;

  const editorRef = useRef<ContentEditorRef>(null);
  const [hasContent, setHasContent] = useState(initialRequest.trim().length > 0);
  const [runtimeId, setRuntimeId] = useState("");

  const draftsQuery = useQuery(issueDraftListOptions(wsId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const start = useStartIssueDraft(wsId);

  const uploadGate = useUploadGate(editorRef);
  const {
    attachments: draftAttachments,
    handleUpload,
    gate,
  } = useIssueCreateUploads("align", uploadGate, editorRef);

  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => editorRef.current?.uploadFile(f)),
  });

  // Set the persisted draft's active mode so a later reopen (and any reader of
  // the unified draft) knows which form the user is editing in.
  useEffect(() => {
    setActiveMode("align");
  }, [setActiveMode]);

  // Defer focus so it lands after the dialog's focus trap has settled.
  useEffect(() => {
    const id = requestAnimationFrame(() => editorRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, []);

  const runtimes = runtimesQuery.data ?? [];
  const usableRuntimes = useMemo(
    () =>
      (runtimesQuery.data ?? []).filter((runtime) =>
        isRuntimeUsableForUser(runtime, currentUserId),
      ),
    [runtimesQuery.data, currentUserId],
  );

  // Picking the runtime moved out of this face with the picker, but choosing
  // one did not: the alignment has to run somewhere, and the user is never
  // asked where. Online first, then the user's own machines, then anything
  // else in the workspace they may use.
  //
  // Online has to outrank "mine", which the picker's own order did not: the
  // list arrives ordered by `created_at` and `isRuntimeUsableForUser` says
  // nothing about status, so "first machine I own" is really "oldest machine I
  // own". The picker could survive seeding that one offline — the user just
  // opened it and chose another. Here there is nothing to open, so seeding an
  // offline machine while an online one sits in the same list would leave the
  // dialog stating it cannot start and offering no way to fix it.
  const seededRuntimeId = useMemo(() => {
    const online = usableRuntimes.filter((runtime) => runtime.status === "online");
    // Falls back to the offline set only so the message below can name a
    // machine; submitting still requires an online one.
    const pool = online.length > 0 ? online : usableRuntimes;
    return (
      (pool.find((runtime) => runtime.owner_id === currentUserId) ?? pool[0])
        ?.id ?? ""
    );
  }, [usableRuntimes, currentUserId]);

  // Fills an empty selection only. The picker seeded through `onSelect` as
  // soon as runtimes arrived (over WS included); a derived seed plus this
  // effect keeps that timing without a mounted picker to call back into.
  useEffect(() => {
    if (runtimeId !== "" || !seededRuntimeId) return;
    setRuntimeId(seededRuntimeId);
  }, [runtimeId, seededRuntimeId]);

  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === runtimeId) ?? null;
  const runtimeOnline = selectedRuntime?.status === "online";
  const runtimesLoading = runtimesQuery.isLoading;
  const hasUsableRuntime = usableRuntimes.length > 0;

  // The banner offers work to RESUME, so the terminal records the same list
  // now carries are filtered out here: "you have 2 unfinished alignments" must
  // count the ones with a next turn in them (DENE-371).
  const drafts: IssueDraftSummary[] = unfinishedIssueDrafts(draftsQuery.data ?? []);
  // `gate` is the coordinator-wide gate: it counts the shared pool's
  // placeholders too, so a file still uploading on the manual face keeps this
  // face's button disabled as well — the first turn binds the same pool.
  const canSubmit = hasContent && runtimeOnline && !start.isPending && !gate.uploading;
  // A create whose response dropped the session id cannot be addressed. The
  // conversation exists, so there is nothing to retry: the banner above is
  // where it can be picked up, and offering the button again is offering a
  // second, duplicate draft. Read off the mutation's own answer rather than a
  // local flag, so the state cannot outlive the request that produced it.
  const unaddressed = start.isSuccess && !start.data.draftId;

  const resume = (draftId: string) => {
    onClose();
    navigation.push(paths.newIssueDraft(draftId));
  };

  const submit = async () => {
    if (!canSubmit || !selectedRuntime || gate.isBlocked() || unaddressed) return;
    const request = editorRef.current?.getMarkdown()?.trim() ?? "";
    if (!request) return;
    // Only the ids whose markdown link the request still references: a file
    // uploaded on the manual face and then removed from the align body must
    // not ride along into the conversation (same rule as the other panels).
    const activeAttachmentIds = draftAttachments
      .filter((attachment) => contentReferencesAttachment(request, attachment))
      .map((attachment) => attachment.id);
    let result: Awaited<ReturnType<typeof start.mutateAsync>>;
    try {
      result = await start.mutateAsync({
        runtimeId: selectedRuntime.id,
        request,
        attachmentIds: activeAttachmentIds.length > 0 ? activeAttachmentIds : undefined,
        // Stored on the draft at creation, so the whole group the conversation
        // settles on is filed under it — a project chosen here is not a display
        // preference the page reads back later.
        projectId,
      });
    } catch {
      // The refusal is already on screen — `start.isError` renders the server's
      // own words above. Swallowing the rejected promise here is what keeps it
      // from also becoming an unhandled rejection with no reader.
      return;
    }
    // The server created the conversation but this response does not carry its
    // id (schema drift, see `useStartIssueDraft`). There is no page to open and
    // the create is NOT a failure to repeat, so the face stays where it is and
    // says so; the draft is in the unfinished list right above.
    if (!result.draftId) return;
    onClose();
    // Navigating even when the first turn failed: the draft exists and holds
    // the request, so staying would only invite the user to create a second
    // one. The page's composer is where a lost turn is resent — and it is told
    // so, because a draft that opens on an empty transcript with no
    // explanation is the failure the user cannot see.
    if (!result.seeded) {
      markIssueDraftSeedFailure(
        result.draftId,
        // The server's own 4xx wording (a runtime that went unusable between
        // the create and the send) is the actionable half, so it travels as
        // given. A 5xx or a transport failure has nothing worth repeating
        // (MUL-6472): an empty reason is the page's cue to say it in its own
        // words rather than to quote the server.
        clientErrorMessage(result.seedError) ?? "",
      );
    }
    navigation.push(paths.newIssueDraft(result.draftId));
  };

  return (
    <>
      <DialogTitle className="sr-only">{t(($) => $.alignment.entry_title)}</DialogTitle>

      {/* `min-h-[140px] flex-1 overflow-y-auto` is the agent panel's proven
          shape (MUL-6236): the region absorbs the delta against the card's
          max height, and the floor keeps a content-driven card from collapsing
          the scroll area to nothing. */}
      <div className="min-h-[140px] flex-1 overflow-y-auto px-6 pt-5 pb-2">
        <UnfinishedIssueDraftsBanner drafts={drafts} onResume={resume} />

        <span className="flex size-11 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <Sparkles className="size-5" aria-hidden="true" />
        </span>
        <h2 className="mt-4 text-title-sm font-semibold">
          {t(($) => $.alignment.entry_title)}
        </h2>
        <p className="mt-2 text-body leading-6 text-muted-foreground">
          {t(($) => $.alignment.entry_description)}
        </p>

        <div
          {...dropZoneProps}
          className="relative mt-4 min-h-32 overflow-y-auto rounded-lg border border-border bg-background px-3 py-2 transition-colors focus-within:border-input"
        >
          <ContentEditor
            ref={editorRef}
            defaultValue={initialRequest}
            placeholder={t(($) => $.alignment.entry_placeholder)}
            onUpdate={(md) => {
              setAlign({ request: md });
              setHasContent(md.trim().length > 0);
            }}
            onSubmit={() => void submit()}
            onUploadFile={handleUpload}
            onUploadingChange={uploadGate.onUploadingChange}
            debounceMs={300}
            attachments={draftAttachments}
          />
          {isDragOver && <FileDropOverlay />}
        </div>

        {!runtimesLoading && !hasUsableRuntime ? (
          <p className="mt-4 text-body text-muted-foreground">
            {t(($) => $.alignment.entry_no_runtime)}
          </p>
        ) : null}

        {/* A seeded but offline runtime blocks submitting, which a disabled
            button alone never explains. Only the machines list can fix it, so
            this stays a statement, not a second action. */}
        {selectedRuntime && !runtimeOnline ? (
          <p role="status" className="mt-4 text-body text-destructive">
            {t(($) => $.alignment.entry_runtime_offline, {
              name: runtimeDisplayName(selectedRuntime),
            })}
          </p>
        ) : null}

        {/* Why it failed, in the server's own words when it has any. Every exit
            from the create — a runtime that is offline, a private runtime
            someone else owns, a draft too large, a workspace this user is not
            in — used to collapse into one sentence that named neither the cause
            nor the fix (DENE-421). Only a 4xx is repeated: a 5xx message is
            internal detail and a transport failure's says nothing actionable,
            so both fall back to this face's own sentence. */}
        {start.isError ? (
          <p role="alert" className="mt-4 text-body text-destructive">
            {clientErrorMessage(start.error) ?? t(($) => $.alignment.entry_failed)}
          </p>
        ) : null}

        {/* The conversation was created but the response did not carry its id,
            so there is no page to open it at. NOT a create to retry: the draft
            exists, it is in the unfinished list above, and a second create is a
            second draft. */}
        {unaddressed ? (
          <p role="alert" className="mt-4 text-body text-destructive">
            {t(($) => $.alignment.entry_unaddressed)}
          </p>
        ) : null}
      </div>

      <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-2.5 border-t px-4 py-3 shrink-0 sm:flex sm:flex-wrap">
        {/* The attach + project pair: both are properties of the issue being
            filed, not of the request, so they stay on the toolbar row rather
            than in front of the one thing this face actually asks for
            (DENE-367). `min-w-0` lets the project pill shrink first when the
            row is tight — its own chrome caps it at 14rem either way. */}
        <div className="flex min-h-7 min-w-0 items-center gap-2 sm:mr-auto">
          <FileUploadButton
            size="sm"
            multiple
            onSelect={(file) => editorRef.current?.uploadFile(file)}
          />
          <ProjectPicker
            projectId={projectId ?? null}
            onUpdate={(updates) => setShared({ projectId: updates.project_id ?? undefined })}
            triggerRender={
              <ClearablePillButton
                onClear={projectId ? () => setShared({ projectId: undefined }) : undefined}
                clearLabel={tProjects(($) => $.picker.clear_aria)}
              />
            }
            align="start"
          />
        </div>
        {/* The way back to filing this as an issue. The body stays in the
            align slot, and the manual face keeps its own — a switch is a
            no-op on the other side's data. */}
        <button
          type="button"
          onClick={() => onSwitchMode?.(null)}
          disabled={gate.uploading}
          aria-disabled={gate.uploading || undefined}
          aria-busy={gate.uploading || undefined}
          title={tModals(($) => $.create_issue.switch_from_align_tooltip)}
          className="flex shrink-0 items-center gap-1.5 justify-self-end text-caption px-2 py-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-accent/60 transition-colors cursor-pointer disabled:cursor-not-allowed disabled:opacity-50"
        >
          <ArrowLeftRight className="size-3.5" />
          {tModals(($) => $.create_issue.switch_from_align)}
        </button>
        <Button variant="ghost" size="sm" onClick={onClose} disabled={start.isPending}>
          {t(($) => $.alignment.entry_cancel)}
        </Button>
        {!runtimesLoading && !hasUsableRuntime ? (
          <Button
            size="sm"
            render={<AppLink href={paths.runtimes()} />}
            nativeButton={false}
          >
            {t(($) => $.alignment.entry_connect_runtime)}
          </Button>
        ) : (
          <Button size="sm" onClick={() => void submit()} disabled={!canSubmit || unaddressed}>
            {start.isPending ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {start.isPending
              ? t(($) => $.alignment.entry_submitting)
              : t(($) => $.alignment.entry_submit)}
          </Button>
        )}
      </div>
    </>
  );
}

/** className for DialogContent in align mode. The shell (which owns the
 *  DialogContent) applies it, so a switch into alignment swaps only the inner
 *  panel — the Portal, Backdrop and Popup stay in the DOM. Exported from here
 *  so the sizing stays next to the face that needs it. */
export function alignDialogContentClass() {
  return cn(
    "p-0 gap-0 flex flex-col overflow-hidden",
    "!top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2",
    "!transition-all !duration-300 !ease-out",
    // Phone gutter — see the matching note in create-issue-dialog.tsx. The
    // height stays content-driven (capped at 80% of the viewport, like the
    // ordinary agent form) so this face opens at the size of its own content
    // instead of a mostly-empty tall card.
    "!w-full !max-w-[calc(100vw-1.5rem)]",
    "!max-h-[80dvh] sm:!max-w-xl",
  );
}
