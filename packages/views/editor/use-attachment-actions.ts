"use client";

import { useCallback, useMemo } from "react";
import { openExternal } from "../platform";
import { useDownloadAttachment } from "./use-download-attachment";

/** What a surface knows about the file the reader asked for. */
export interface AttachmentTarget {
  /** Attachment id once the file resolves to a record. */
  attachmentId?: string | null;
  /** The URL as rendered, for files with no record (external links). */
  url?: string;
}

export interface AttachmentActions {
  /**
   * Hand the file to the reader: a known attachment is re-signed and
   * downloaded; a bare URL opens externally.
   */
  download: (target: AttachmentTarget) => void;
}

/**
 * The one place that decides what happens when a reader asks for a file's
 * bytes. File cards, the preview viewer, deliverables and markdown links all
 * come through here instead of calling the download hook or `openExternal`
 * themselves.
 */
export function useAttachmentActions(): AttachmentActions {
  const downloadById = useDownloadAttachment();
  const download = useCallback(
    ({ attachmentId, url }: AttachmentTarget) => {
      if (attachmentId) {
        void downloadById(attachmentId);
        return;
      }
      if (url) openExternal(url);
    },
    [downloadById],
  );
  return useMemo(() => ({ download }), [download]);
}
