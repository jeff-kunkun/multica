// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isRetryableUploadError, retryUpload } from "./upload-retry";

class HttpError extends Error {
  constructor(public status: number) {
    super(`HTTP ${status}`);
  }
}

describe("isRetryableUploadError", () => {
  it("treats network failures, timeouts and 5xx as transient", () => {
    expect(isRetryableUploadError(new TypeError("Failed to fetch"))).toBe(true);
    expect(isRetryableUploadError(new HttpError(524))).toBe(true);
    expect(isRetryableUploadError(new HttpError(429))).toBe(true);
    expect(isRetryableUploadError(new HttpError(413))).toBe(false);
    expect(isRetryableUploadError(Object.assign(new Error("x"), { name: "AbortError" }))).toBe(false);
  });
});

describe("retryUpload", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("keeps retrying a visible, online page well past the old 2.5s window", async () => {
    // Offline for ~40s with the browser still reporting online: every attempt
    // fails with a 524 / network error until the connection comes back.
    let calls = 0;
    const attempt = vi.fn(async () => {
      calls += 1;
      if (calls <= 8) throw calls % 2 ? new TypeError("Failed to fetch") : new HttpError(524);
      return "ok";
    });

    const result = retryUpload(attempt);
    await vi.advanceTimersByTimeAsync(60_000);

    await expect(result).resolves.toBe("ok");
    expect(calls).toBe(9);
  });

  it("gives up only after the stall window passes with no new bytes", async () => {
    const attempt = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const result = retryUpload(attempt, { stallTimeoutMs: 30_000 });
    const settled = expect(result).rejects.toThrow("Failed to fetch");

    await vi.advanceTimersByTimeAsync(29_000);
    const before = attempt.mock.calls.length;
    await vi.advanceTimersByTimeAsync(30_000);
    await settled;
    expect(before).toBeGreaterThan(3);
  });

  it("resets the stall clock whenever new bytes land", async () => {
    let uploaded = 0;
    const attempt = vi.fn(async (onProgress: (u: number, t: number) => void) => {
      uploaded += 1;
      onProgress(uploaded, 100);
      if (uploaded < 10) throw new TypeError("Failed to fetch");
      return "done";
    });
    const result = retryUpload(attempt, { stallTimeoutMs: 5_000, delaysMs: [4_000] });
    await vi.advanceTimersByTimeAsync(60_000);
    await expect(result).resolves.toBe("done");
  });

  it("fails immediately on a permanent error", async () => {
    const attempt = vi.fn(async () => {
      throw new HttpError(413);
    });
    await expect(retryUpload(attempt)).rejects.toThrow("HTTP 413");
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  it("stops waiting when aborted", async () => {
    const controller = new AbortController();
    const attempt = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    const result = retryUpload(attempt, { signal: controller.signal });
    const settled = expect(result).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(100);
    controller.abort();
    await settled;
  });

  it("retries at once when the page comes back online", async () => {
    let calls = 0;
    const attempt = vi.fn(async () => {
      calls += 1;
      if (calls === 1) throw new TypeError("Failed to fetch");
      return "ok";
    });
    const result = retryUpload(attempt, { delaysMs: [60_000] });
    await vi.advanceTimersByTimeAsync(0);
    window.dispatchEvent(new Event("online"));
    await vi.advanceTimersByTimeAsync(0);
    await expect(result).resolves.toBe("ok");
  });
});
