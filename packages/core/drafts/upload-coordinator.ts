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

export interface UploadCoordinatorContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
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
  onProgress?: (uploadedBytes: number, totalBytes: number) => void;
  /**
   * Settled outcome. NOT called on abort — an aborted upload leaves its
   * placeholder in `uploading`, which the store drops on the next load (aborts
   * happen on logout, where the placeholder is cleared anyway). The caller MUST
   * re-check its draft still tracks `clientUploadId` before writing the result.
   */
  onSettled: (outcome: UploadOutcome) => void;
}

const controllers = new Map<string, AbortController>();
const MAX_RECOVERY_RETRIES = 3;
const RECOVERY_DELAYS_MS = [250, 750, 1500] as const;

function isRetryableUploadError(error: unknown): boolean {
  if (!(error instanceof Error) || error.name === "AbortError") return false;
  const candidate = error as Error & { retryable?: boolean; status?: number };
  if (candidate.retryable === false) return false;
  if (candidate.retryable === true) return true;
  if (typeof candidate.status === "number") {
    return candidate.status === 408 || candidate.status === 425 || candidate.status === 429 || candidate.status >= 500;
  }
  return /network|fetch|timeout|offline|connection|temporarily unavailable/i.test(error.message) || error.name === "TypeError";
}

/**
 * Wait for a short retry window while also listening for the two browser
 * signals that commonly explain an interrupted mobile upload. The timer is
 * intentionally used while the page reports visible/online: Safari can keep
 * `navigator.onLine === true` after the radio has dropped, so a retry timer is
 * still needed even when no `online` event will fire.
 */
function waitForRecovery(controller: AbortController, delayMs: number): Promise<boolean> {
  if (controller.signal.aborted) return Promise.resolve(false);
  if (typeof document === "undefined" || typeof window === "undefined") {
    return new Promise((resolve) => {
      const timer = setTimeout(() => resolve(!controller.signal.aborted), delayMs);
      controller.signal.addEventListener("abort", () => {
        clearTimeout(timer);
        resolve(false);
      }, { once: true });
    });
  }
  return new Promise((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let settled = false;
    const cleanup = () => {
      if (timer !== undefined) clearTimeout(timer);
      document.removeEventListener("visibilitychange", resume);
      window.removeEventListener("online", resume);
      controller.signal.removeEventListener("abort", aborted);
    };
    const finish = (ok: boolean) => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve(ok);
    };
    const resume = () => {
      if (document.hidden || navigator.onLine === false) return;
      finish(true);
    };
    const aborted = () => finish(false);
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("online", resume);
    controller.signal.addEventListener("abort", aborted, { once: true });
    if (!document.hidden && navigator.onLine !== false) {
      timer = setTimeout(() => finish(true), delayMs);
    }
    resume();
  });
}

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
  onProgress,
  onSettled,
}: StartUploadArgs): void {
  const controller = new AbortController();
  controllers.set(clientUploadId, controller);

  void (async () => {
    let recoveryRetries = 0;
    try {
      while (true) {
        try {
          const attachment = await api.uploadFile(
            file,
            {
              issueId: ctx?.issueId,
              commentId: ctx?.commentId,
              chatSessionId: ctx?.chatSessionId,
              onProgress,
            },
            controller.signal,
          );
          onSettled({ clientUploadId, status: "uploaded", attachment });
          return;
        } catch (err) {
          // An abort is not a failure: leave the placeholder untouched. It
          // stays uploading until the caller clears it during logout.
          if (controller.signal.aborted || (err instanceof Error && err.name === "AbortError")) {
            logger.info("upload aborted", { clientUploadId });
            return;
          }
          if (!isRetryableUploadError(err) || recoveryRetries >= MAX_RECOVERY_RETRIES) {
            onSettled({
              clientUploadId,
              status: "failed",
              error: err instanceof Error ? err : new Error("Upload failed"),
            });
            return;
          }
          const delay = RECOVERY_DELAYS_MS[Math.min(recoveryRetries, RECOVERY_DELAYS_MS.length - 1)]!;
          recoveryRetries += 1;
          if (!(await waitForRecovery(controller, delay))) return;
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
