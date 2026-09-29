import type { ApiClient } from "../api/client";
import type { Attachment } from "../types";
import { createLogger } from "../logger";

/**
 * Module-level file-upload coordinator (MUL-5181, L2).
 *
 * Ownership inversion: an upload is owned here, NOT by the React component that
 * started it. The composer only records a persisted placeholder (a
 * `DraftUpload`) in its draft and hands the file to `startUpload`; the request
 * then outlives the composer's mount. Closing the editor/modal no longer aborts
 * the upload — its result lands in the persisted draft through `onSettled`, so
 * reopening the composer shows the attachment.
 *
 * The coordinator itself knows nothing about drafts. `onSettled` fires with the
 * outcome and the CALLER is responsible for the generation guard: it must check
 * that its draft still tracks `clientUploadId` before writing (the draft may
 * have been cleared/submitted, or the placeholder removed, while the request
 * was in flight).
 *
 * Abort (`abortAll`) is the one thing a plain fire-and-forget promise cannot do:
 * on logout, every tracked request is cancelled so the previous user's bytes
 * never bind to an attachment under the next session.
 */

const logger = createLogger("drafts.upload-coordinator");

// The API client already makes three immediate attempts for each chunk. Keep
// the same coordinator-owned upload alive after that short ladder for
// transport/proxy failures. The API's persisted upload id means every later
// attempt only sends missing chunks. There is deliberately no terminal limit:
// navigator.onLine can remain true during a broken route or Cloudflare 524.
const RETRY_DELAYS_MS = [250, 750, 1500, 2000] as const;

function isRetryableUploadError(error: unknown): boolean {
  if (error instanceof Error && error.name === "AbortError") return false;
  const status =
    typeof error === "object" && error !== null && "status" in error
      ? Number((error as { status?: unknown }).status)
      : NaN;
  if ([408, 425, 429, 500, 502, 503, 504, 524].includes(status)) return true;
  const message = error instanceof Error ? error.message : String(error);
  return /(?:failed to fetch|network|offline|timed? ?out|timeout|connection|(?:^|\D)524(?:\D|$))/i.test(
    message,
  );
}

function waitForUploadRetry(controller: AbortController, delayMs: number): Promise<void> {
  return new Promise((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      if (timer !== undefined) clearTimeout(timer);
      if (typeof window !== "undefined") window.removeEventListener("online", onOnline);
      if (typeof document !== "undefined") document.removeEventListener("visibilitychange", onVisibility);
      controller.signal.removeEventListener("abort", finish);
      resolve();
    };
    const onVisibility = () => {
      if (typeof document === "undefined" || !document.hidden) finish();
    };
    const onOnline = () => {
      if (typeof document !== "undefined" && document.hidden) return;
      finish();
    };

    controller.signal.addEventListener("abort", finish, { once: true });
    if (typeof window !== "undefined") window.addEventListener("online", onOnline, { once: true });
    if (typeof document !== "undefined" && document.hidden) {
      document.addEventListener("visibilitychange", onVisibility);
      return;
    }
    if (typeof navigator !== "undefined" && navigator.onLine === false) return;
    timer = setTimeout(finish, delayMs);
  });
}

export interface UploadCoordinatorContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
  onProgress?: (uploadedBytes: number, totalBytes: number) => void;
}

export type UploadOutcome =
  | { clientUploadId: string; status: "uploaded"; attachment: Attachment }
  | { clientUploadId: string; status: "failed"; error: Error };

export interface StartUploadArgs {
  /** Client-minted id tying this request to its persisted `DraftUpload`. */
  clientUploadId: string;
  file: File;
  /** Injected so the coordinator is framework-agnostic and unit-testable. */
  api: Pick<ApiClient, "uploadFile">;
  ctx?: UploadCoordinatorContext;
  /**
   * Settled outcome. NOT called on abort — an aborted upload leaves its
   * placeholder in `uploading`, which the store drops on the next load (aborts
   * happen on logout, where the placeholder is cleared anyway). The caller MUST
   * re-check its draft still tracks `clientUploadId` before writing the result.
   */
  onSettled: (outcome: UploadOutcome) => void;
}

const controllers = new Map<string, AbortController>();

/**
 * Start an upload owned by this module. Returns immediately; the outcome is
 * delivered through `onSettled`. Safe to call for a `clientUploadId` already in
 * flight (the newer controller replaces the map entry — callers mint unique
 * ids, so this is a defensive no-op in practice).
 */
export function startUpload({
  clientUploadId,
  file,
  api,
  ctx,
  onSettled,
}: StartUploadArgs): void {
  const controller = new AbortController();
  controllers.set(clientUploadId, controller);

  void (async () => {
    let retryAttempt = 0;
    try {
      while (!controller.signal.aborted) {
        try {
          const attachment = await api.uploadFile(
            file,
            {
              issueId: ctx?.issueId,
              commentId: ctx?.commentId,
              chatSessionId: ctx?.chatSessionId,
              onProgress: ctx?.onProgress,
            },
            controller.signal,
          );
          onSettled({ clientUploadId, status: "uploaded", attachment });
          return;
        } catch (err) {
          if (controller.signal.aborted || (err instanceof Error && err.name === "AbortError")) {
            logger.info("upload aborted", { clientUploadId });
            return;
          }
          if (!isRetryableUploadError(err)) {
            onSettled({
              clientUploadId,
              status: "failed",
              error: err instanceof Error ? err : new Error("Upload failed"),
            });
            return;
          }

          const delayMs =
            RETRY_DELAYS_MS[Math.min(retryAttempt, RETRY_DELAYS_MS.length - 1)] ?? 2000;
          retryAttempt += 1;
          logger.info("retrying upload after transient failure", {
            clientUploadId,
            delayMs,
            attempt: retryAttempt,
          });
          // Visibility resumes are handled here too, so a backgrounded mobile
          // page retries the same file/session when it returns to the front.
          await waitForUploadRetry(controller, delayMs);
        }
      }
    } finally {
      // Only drop the entry if it is still ours — a racing re-start under the
      // same id must not have its controller evicted by our finally.
      if (controllers.get(clientUploadId) === controller) {
        controllers.delete(clientUploadId);
      }
    }
  })();
}

/** Abort a single tracked upload, if present. */
export function abortUpload(clientUploadId: string): void {
  const controller = controllers.get(clientUploadId);
  if (!controller) return;
  controllers.delete(clientUploadId);
  controller.abort();
}

/**
 * Abort every tracked upload. Called on logout (before drafts are cleared) so
 * no in-flight upload can bind an attachment under the next session.
 */
export function abortAll(): void {
  if (controllers.size === 0) return;
  logger.info("aborting all uploads", { count: controllers.size });
  for (const controller of controllers.values()) {
    controller.abort();
  }
  controllers.clear();
}

/** Test-only: number of uploads currently tracked. */
export function __trackedUploadCountForTest(): number {
  return controllers.size;
}
