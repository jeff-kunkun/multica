"use client";

import { useCallback, useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useDefaultLayout } from "react-resizable-panels";
import { ArrowLeft } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { decodeIssueDraftInput, stripIssueDraftDirectives } from "@multica/core/issue-drafts";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { ChatMessage } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import { useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { IssueDraftConversation } from "./issue-draft-conversation";
import { IssueDraftPolicyPicker } from "./issue-draft-policy-picker";
import { IssueDraftPreviewPanel } from "./issue-draft-preview-panel";
import { IssueDraftStageStrip } from "./issue-draft-stage-strip";
import { useIssueDraftSession } from "./use-issue-draft-session";

/**
 * One alignment conversation, addressed by its own draft id.
 *
 * The id is in the path rather than in component state because this
 * conversation outlives the screen that started it: a refresh, a back/forward,
 * a reopened desktop tab and a direct link all have to land back in the same
 * alignment. Confirming is the only thing that creates an issue.
 */
export function IssueDraftPage({ draftId }: { draftId: string }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const backOrReplace = useBackOrReplace();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);

  const session = useIssueDraftSession(draftId);
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_issue_draft_layout",
  });

  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const membersQuery = useQuery(memberListOptions(wsId));

  /**
   * The transcript as a person should read it.
   *
   * Both directions of the carrier's wire format are undone here: the user's
   * own turns are JSON envelopes carrying the draft they were asking about, and
   * the carrier's replies end in an `<issue_draft>` block. Rendered raw, neither
   * is a conversation.
   */
  const displayMessages = useMemo<ChatMessage[]>(
    () =>
      session.messages.map((message) => ({
        ...message,
        content:
          message.role === "user"
            ? decodeIssueDraftInput(message.content)
            : stripIssueDraftDirectives(message.content),
      })),
    [session.messages],
  );

  const leave = useCallback(
    () => backOrReplace(paths.issues()),
    [backOrReplace, paths],
  );

  // The conversation is gone — discarded elsewhere, or a link that outlived it.
  // Replace rather than push: the address no longer resolves, so it must not
  // stay on the stack for a back to land on.
  useEffect(() => {
    if (session.missing) navigation.replace(paths.issues());
  }, [navigation, paths, session.missing]);

  // A confirmed draft is final. Replace, for the same reason: the alignment URL
  // is no longer an unfinished draft once the issue exists.
  useEffect(() => {
    if (session.createdIssueId) {
      navigation.replace(paths.issueDetail(session.createdIssueId));
    }
  }, [navigation, paths, session.createdIssueId]);

  if (session.missing) return null;

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <header className="shrink-0">
        <div className="flex items-center gap-3 px-5 pt-4">
          <Button variant="ghost" size="sm" onClick={leave}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t(($) => $.alignment.back)}
          </Button>
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-title-sm font-semibold tracking-tight">
              {session.draft?.title.trim() || t(($) => $.alignment.title)}
            </h1>
            <p className="truncate text-caption text-muted-foreground">
              {t(($) => $.alignment.subtitle)}
            </p>
          </div>
          <IssueDraftPolicyPicker
            policy={session.policy}
            switching={session.switchingPolicy}
            // A conversation that is mid-reply or already confirmed has no next
            // turn to change, so the picker is not offered one.
            disabled={session.pending || session.stage === "creating" || session.stage === "created"}
            onChange={(policy) => void session.setPolicy(policy)}
          />
        </div>
        <IssueDraftStageStrip
          stage={session.stage}
          hasTitle={!!session.draft?.title.trim()}
        />
      </header>

      {session.retired ? (
        <RetiredPanel onLeave={leave} />
      ) : session.loadFailed ? (
        <FailedPanel
          message={t(($) => $.alignment.load_failed)}
          onRetry={session.retry}
        />
      ) : (
        <ResizablePanelGroup
          orientation="horizontal"
          className="min-h-0 flex-1"
          defaultLayout={defaultLayout}
          onLayoutChanged={onLayoutChanged}
        >
          <ResizablePanel id="conversation" minSize="30%">
            <IssueDraftConversation
              draftId={draftId}
              messages={displayMessages}
              loading={session.messagesLoading}
              pendingTask={session.pendingTask}
              runtimeOnline={session.runtimeOnline}
              sending={session.sending}
              onSend={session.send}
              onStop={() => void session.stop()}
              error={session.error}
              question={session.question}
            />
          </ResizablePanel>
          <ResizableHandle />
          <ResizablePanel
            id="preview"
            defaultSize={420}
            minSize={340}
            groupResizeBehavior="preserve-pixel-size"
          >
            <IssueDraftPreviewPanel
              draft={session.draft}
              stage={session.stage}
              canConfirm={session.canConfirm}
              saving={session.saving}
              saved={session.saved}
              confirming={session.confirming}
              abandoning={session.abandoning}
              runtime={session.runtime}
              runtimes={runtimesQuery.data ?? []}
              runtimesLoading={runtimesQuery.isLoading}
              members={membersQuery.data ?? []}
              currentUserId={currentUserId}
              switchingRuntime={session.switchingRuntime}
              pending={session.pending}
              onDirtyChange={session.setLocalDirty}
              onSave={session.save}
              onGenerate={session.generatePreview}
              onConfirm={session.confirm}
              onAbandon={async () => {
                const abandoned = await session.abandon();
                if (abandoned) leave();
                return abandoned;
              }}
              onSwitchRuntime={session.switchRuntime}
            />
          </ResizablePanel>
        </ResizablePanelGroup>
      )}
    </div>
  );
}

function RetiredPanel({ onLeave }: { onLeave: () => void }) {
  const { t } = useT("issues");
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-5 py-10">
      <div className="w-full max-w-md text-center">
        <h2 className="text-title-sm font-semibold">
          {t(($) => $.alignment.retired_title)}
        </h2>
        <p className="mt-2 text-body leading-6 text-muted-foreground">
          {t(($) => $.alignment.retired_description)}
        </p>
        <Button className="mt-5" onClick={onLeave}>
          {t(($) => $.alignment.back)}
        </Button>
      </div>
    </div>
  );
}

function FailedPanel({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  const { t } = useT("issues");
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-5 py-10">
      <div className="w-full max-w-md text-center">
        <p role="alert" className="text-body text-destructive">
          {message}
        </p>
        <Button className="mt-5" variant="outline" onClick={onRetry}>
          {t(($) => $.alignment.retry)}
        </Button>
      </div>
    </div>
  );
}
