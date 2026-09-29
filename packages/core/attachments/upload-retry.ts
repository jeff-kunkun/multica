/**
 * One retry policy for every web/desktop upload path (the draft coordinator
 * behind chat and comments, and the plain `useFileUpload` hook behind the
 * issue description).
 *
 * A dropped connection must not cost the user their placeholder: the chunked
 * upload remembers its server session, so each retry only sends the chunks
 * the server is still missing. We therefore keep retrying transient failures
 * (network error, timeout, 5xx such as Cloudflare's 524, 408/425/429) for as
 * long as the upload keeps making progress, and only give up after
 * `stallTimeoutMs` of failures with no new bytes arriving. Coming back online
 * or returning to the foreground skips the remaining backoff.
 */

export type UploadProgress = (uploadedBytes: number, totalBytes: number) => void;

export const UPLOAD_RETRY_DELAYS_MS = [250, 750, 1500, 3000, 5000, 10000, 15000] as const;
export const UPLOAD_STALL_TIMEOUT_MS = 10 * 60 * 1000;

export function isRetryableUploadError(error: unknown): boolean {
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
 * Wait `delayMs`, or less if the page comes back online / to the foreground.
 * While the page is hidden or offline the timer does not run: the retry waits
 * for the `visibilitychange` / `online` event instead. Safari can keep
 * `navigator.onLine === true` after the radio has dropped, so a visible,
 * "online" page still retries on the timer. Resolves false when aborted.
 */
export function waitForUploadRecovery(delayMs: number, signal?: AbortSignal): Promise<boolean> {
  if (signal?.aborted) return Promise.resolve(false);
  return new Promise((resolve) => {
    const hasDom = typeof document !== "undefined" && typeof window !== "undefined";
    let timer: ReturnType<typeof setTimeout> | undefined;
    let settled = false;
    const finish = (ok: boolean) => {
      if (settled) return;
      settled = true;
      if (timer !== undefined) clearTimeout(timer);
      if (hasDom) {
        document.removeEventListener("visibilitychange", resume);
        window.removeEventListener("online", resume);
      }
      signal?.removeEventListener("abort", aborted);
      resolve(ok);
    };
    const reachable = () => !hasDom || (!document.hidden && navigator.onLine !== false);
    const resume = () => {
      if (reachable()) finish(true);
    };
    const aborted = () => finish(false);
    signal?.addEventListener("abort", aborted, { once: true });
    if (hasDom) {
      document.addEventListener("visibilitychange", resume);
      window.addEventListener("online", resume);
    }
    if (reachable()) timer = setTimeout(() => finish(true), delayMs);
  });
}

export interface RetryUploadOptions {
  signal?: AbortSignal;
  onProgress?: UploadProgress;
  stallTimeoutMs?: number;
  delaysMs?: readonly number[];
  now?: () => number;
}

/**
 * Run `attempt` until it succeeds, fails permanently, or stalls. Throws the
 * last error when giving up; throws an AbortError when `signal` aborts.
 */
export async function retryUpload<T>(
  attempt: (onProgress: UploadProgress) => Promise<T>,
  {
    signal,
    onProgress,
    stallTimeoutMs = UPLOAD_STALL_TIMEOUT_MS,
    delaysMs = UPLOAD_RETRY_DELAYS_MS,
    now = Date.now,
  }: RetryUploadOptions = {},
): Promise<T> {
  let bestUploaded = -1;
  let lastProgressAt = now();
  let retries = 0;
  const track: UploadProgress = (uploaded, total) => {
    if (uploaded > bestUploaded) {
      bestUploaded = uploaded;
      lastProgressAt = now();
      // New bytes landed: a later failure starts its backoff from the top.
      retries = 0;
    }
    onProgress?.(uploaded, total);
  };
  for (;;) {
    try {
      return await attempt(track);
    } catch (error) {
      if (signal?.aborted) throw error;
      if (!isRetryableUploadError(error) || now() - lastProgressAt >= stallTimeoutMs) throw error;
      const delay = delaysMs[Math.min(retries, delaysMs.length - 1)]!;
      retries += 1;
      if (!(await waitForUploadRecovery(delay, signal))) {
        throw Object.assign(new Error("Upload aborted"), { name: "AbortError" });
      }
    }
  }
}
