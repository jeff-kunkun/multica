"use client";

/**
 * Issue-create binding for the coordinated-upload engine (MUL-5181, L2).
 *
 * All three create panels (manual form, agent prompt, alignment request) share
 * ONE upload pool — `draft.shared.attachments` — so a file survives a mode
 * switch from any side. What differs per mode is the BODY the upload's link
 * belongs to: the manual description, the agent prompt, or the alignment
 * request. The binding captures the mode that started the upload, so a settle
 * that arrives after the dialog closed (or after the user flipped modes)
 * writes the link back into the body it was pasted into, never another side's.
 *
 * Uploads here carry no issue/comment context — the issue does not exist yet;
 * the server binds attachments at create time from `attachment_ids`.
 */

import { useMemo, type RefObject } from "react";
import { attachmentToDraftUpload, type DraftUpload } from "@multica/core/drafts";
import { useIssueDraftStore } from "@multica/core/issues/stores";
import type { CreateMode } from "@multica/core/issues/stores";
import type { UploadGate } from "../editor/use-upload-gate";
import type { ContentEditorRef } from "../editor/content-editor";
import {
  useCoordinatedUploads,
  type CoordinatedUploads,
  type UploadDraftBinding,
} from "../editor/use-coordinated-uploads";

function appendMarkdown(existing: string, markdown: string): string {
  return existing.trim() ? `${existing.replace(/\s+$/, "")}\n\n${markdown}` : markdown;
}

function sharedUploads(): DraftUpload[] {
  return useIssueDraftStore.getState().draft.shared.attachments ?? [];
}

function setSharedUploads(next: DraftUpload[]): void {
  useIssueDraftStore.getState().setShared({ attachments: next });
}

export function useIssueCreateUploads(
  mode: CreateMode,
  editorGate: UploadGate,
  editorRef: RefObject<ContentEditorRef | null>,
): CoordinatedUploads {
  const uploads = useIssueDraftStore((s) => s.draft.shared.attachments);

  const binding = useMemo<UploadDraftBinding>(
    () => ({
      registryKey: `issue-create:${mode}`,
      getUploads: sharedUploads,
      addUpload: (u) => {
        const cur = sharedUploads();
        if (cur.some((x) => x.clientUploadId === u.clientUploadId)) return;
        setSharedUploads([...cur, u]);
      },
      settleUpload: (id, att) => {
        const cur = sharedUploads();
        if (!cur.some((u) => u.clientUploadId === id)) return;
        // attachmentToDraftUpload strips the response-scoped signed
        // download_url before the row is persisted.
        setSharedUploads(
          cur.map((u) =>
            u.clientUploadId === id ? { ...attachmentToDraftUpload(att), clientUploadId: id } : u,
          ),
        );
      },
      failUpload: (id, error) => {
        const cur = sharedUploads();
        const target = cur.find((u) => u.clientUploadId === id);
        if (!target) return;
        setSharedUploads(
          cur.map((u) =>
            u.clientUploadId === id
              ? {
                  clientUploadId: id,
                  status: "failed",
                  filename: target.filename,
                  size: target.size,
                  contentType: target.contentType,
                  error,
                }
              : u,
          ),
        );
      },
      removeUpload: (id) => {
        const cur = sharedUploads();
        if (!cur.some((u) => u.clientUploadId === id)) return;
        setSharedUploads(cur.filter((u) => u.clientUploadId !== id));
      },
      getBody: () => {
        const { draft } = useIssueDraftStore.getState();
        if (mode === "agent") return draft.agent.prompt;
        if (mode === "align") return draft.align.request;
        return draft.manual.description;
      },
      appendToBody: (md) => {
        const state = useIssueDraftStore.getState();
        if (mode === "agent") {
          state.setAgent({ prompt: appendMarkdown(state.draft.agent.prompt, md) });
        } else if (mode === "align") {
          state.setAlign({ request: appendMarkdown(state.draft.align.request, md) });
        } else {
          state.setManual({ description: appendMarkdown(state.draft.manual.description, md) });
        }
      },
    }),
    [mode],
  );

  return useCoordinatedUploads(binding, uploads, {}, editorGate, editorRef);
}
