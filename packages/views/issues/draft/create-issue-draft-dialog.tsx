"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Sparkles } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDraftListOptions, useStartIssueDraft } from "@multica/core/issue-drafts";
import { useWorkspacePaths } from "@multica/core/paths";
import { isRuntimeUsableForUser, runtimeListOptions } from "@multica/core/runtimes";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { IssueDraftSummary } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { RuntimePicker } from "../../agents/components/runtime-picker";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { UnfinishedIssueDraftsBanner } from "./unfinished-issue-drafts";

/**
 * The entry point to aligning: describe the request, then leave.
 *
 * This dialog's whole job is to open the conversation and hand off — it awaits
 * the server's session id, sends the user's own first turn, and navigates to
 * the alignment page. The conversation itself is deliberately NOT here: it is a
 * durable object that is left and resumed, and a modal cannot be refreshed
 * into, linked to, or restored as a desktop tab.
 */
export function CreateIssueDraftDialog({
  onClose,
  data,
}: {
  onClose: () => void;
  data?: Record<string, unknown> | null;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);

  const seededRequest =
    typeof data?.initial_description === "string" ? data.initial_description : "";
  const [request, setRequest] = useState(seededRequest);
  const [runtimeId, setRuntimeId] = useState("");

  const draftsQuery = useQuery(issueDraftListOptions(wsId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const membersQuery = useQuery(memberListOptions(wsId));
  const start = useStartIssueDraft(wsId);

  const runtimes = runtimesQuery.data ?? [];
  const usableRuntimes = runtimes.filter((runtime) =>
    isRuntimeUsableForUser(runtime, currentUserId),
  );
  // No seeding effect here: RuntimePicker is the sole source of truth for
  // filling an empty selection, and it only fires while the value is empty, so
  // a second seeding path could only disagree with it.
  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === runtimeId) ?? null;

  const drafts: IssueDraftSummary[] = draftsQuery.data ?? [];
  const canSubmit =
    request.trim().length > 0 &&
    selectedRuntime?.status === "online" &&
    !start.isPending;

  const resume = (draftId: string) => {
    onClose();
    navigation.push(paths.newIssueDraft(draftId));
  };

  const submit = async () => {
    if (!canSubmit || !selectedRuntime) return;
    const result = await start.mutateAsync({
      runtimeId: selectedRuntime.id,
      request,
    });
    onClose();
    // Navigating even when the first turn failed: the draft exists and holds
    // the request, so staying would only invite the user to create a second
    // one. The page's composer is where a lost turn is resent.
    navigation.push(paths.newIssueDraft(result.draftId));
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="gap-0 p-0 sm:max-w-xl">
        <div className="max-h-[80dvh] overflow-y-auto px-6 py-6">
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

          <div className="mt-5 space-y-4">
            <Textarea
              value={request}
              rows={4}
              autoFocus
              aria-label={t(($) => $.alignment.entry_placeholder)}
              placeholder={t(($) => $.alignment.entry_placeholder)}
              onChange={(event) => setRequest(event.target.value)}
            />

            <div className="space-y-2">
              <span className="text-caption text-muted-foreground">
                {t(($) => $.alignment.entry_runtime)}
              </span>
              {runtimesQuery.isLoading || usableRuntimes.length > 0 ? (
                <RuntimePicker
                  runtimes={runtimes}
                  runtimesLoading={runtimesQuery.isLoading}
                  members={membersQuery.data ?? []}
                  currentUserId={currentUserId}
                  selectedRuntimeId={runtimeId}
                  onSelect={setRuntimeId}
                  disabled={start.isPending}
                />
              ) : (
                <p className="text-body text-muted-foreground">
                  {t(($) => $.alignment.entry_no_runtime)}
                </p>
              )}
            </div>

            {start.isError ? (
              <p role="alert" className="text-body text-destructive">
                {t(($) => $.alignment.entry_failed)}
              </p>
            ) : null}
          </div>

          <div className="mt-6 flex items-center justify-end gap-2">
            <Button variant="ghost" onClick={onClose} disabled={start.isPending}>
              {t(($) => $.alignment.entry_cancel)}
            </Button>
            {usableRuntimes.length === 0 && !runtimesQuery.isLoading ? (
              <Button
                render={<AppLink href={paths.runtimes()} />}
                nativeButton={false}
              >
                {t(($) => $.alignment.entry_connect_runtime)}
              </Button>
            ) : (
              <Button onClick={() => void submit()} disabled={!canSubmit}>
                {start.isPending ? (
                  <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                ) : null}
                {start.isPending
                  ? t(($) => $.alignment.entry_submitting)
                  : t(($) => $.alignment.entry_submit)}
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
