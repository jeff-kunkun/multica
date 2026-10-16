/**
 * Determine whether web navigation has a safe in-app back destination.
 * Use Navigation API entries for committed same-origin history. history.length
 * includes external entries, and counting router.push requests includes navigations
 * that may be replaced or abandoned. Browsers without the Navigation API (Safari
 * before 26.2) read the depth `installInAppHistoryDepth` stamps on every committed
 * `pushState` / `replaceState` entry: a push is one deeper than the entry it
 * leaves, a replace keeps the depth, and an entry the app never wrote reads as
 * depth 0. Return false when the browser cannot prove a destination so callers
 * use their fallback instead of leaving the app.
 */

/** Minimal shape of the Navigation API — not in TypeScript's DOM lib yet. */
type NavigationApi = { canGoBack: boolean };

const DEPTH_KEY = "__multicaDepth";
const BASE_KEY = "__multicaBase";
const INSTALLED = Symbol.for("multica.inAppHistoryDepth");

function depthOf(state: unknown): number {
  if (state === null || typeof state !== "object") return 0;
  const depth = (state as Record<string, unknown>)[DEPTH_KEY];
  return typeof depth === "number" && Number.isInteger(depth) && depth > 0
    ? depth
    : 0;
}

function baseOf(state: unknown): string | null {
  if (state === null || typeof state !== "object") return null;
  const base = (state as Record<string, unknown>)[BASE_KEY];
  return typeof base === "string" ? base : null;
}

/**
 * Copy of `data` carrying `depth` and the path of the entry at the bottom of
 * the in-app run; non-object state is left unstamped.
 */
function stamp(data: unknown, depth: number, base: string): unknown {
  if (data === null || data === undefined) {
    return { [DEPTH_KEY]: depth, [BASE_KEY]: base };
  }
  if (typeof data !== "object" || Array.isArray(data)) return data;
  return { ...(data as Record<string, unknown>), [DEPTH_KEY]: depth, [BASE_KEY]: base };
}

function pathnameOf(url: string | URL | null | undefined): string {
  if (url === null || url === undefined) return window.location.pathname;
  try {
    return new URL(String(url), window.location.href).pathname;
  } catch {
    return window.location.pathname;
  }
}

/**
 * Wrap `history.pushState` / `replaceState` so every committed entry records
 * its in-app depth. Idempotent; safe to call from an effect on every mount.
 */
export function installInAppHistoryDepth(): void {
  if (typeof window === "undefined") return;
  const history = window.history as History & { [INSTALLED]?: true };
  if (history[INSTALLED]) return;
  history[INSTALLED] = true;
  const push = history.pushState;
  const replace = history.replaceState;
  history.pushState = function pushState(data, unused, url) {
    const base = baseOf(history.state) ?? window.location.pathname;
    return push.call(this, stamp(data, depthOf(history.state) + 1, base), unused, url);
  };
  history.replaceState = function replaceState(data, unused, url) {
    const depth = depthOf(history.state);
    // Replacing the bottom entry changes what the bottom is.
    const base = depth === 0 ? pathnameOf(url) : (baseOf(history.state) ?? pathnameOf(url));
    return replace.call(this, stamp(data, depth, base), unused, url);
  };
}

/**
 * Whether a step back stays inside the app: the Navigation API's answer when
 * the browser has one, otherwise the depth stamped on the current entry.
 */
export function canGoBackInApp(): boolean {
  if (typeof window === "undefined") return false;
  const navigation = (window as { navigation?: unknown }).navigation as
    | NavigationApi
    | undefined;
  if (navigation && typeof navigation === "object" && "canGoBack" in navigation) {
    return navigation.canGoBack === true;
  }
  return depthOf(window.history.state) > 0;
}

/**
 * Leave the page under the current one as `homePath`, so Back from a page
 * opened cold (a shared task link, a notification) lands on it instead of
 * leaving the app. The current entry becomes `homePath`, and the page the user
 * is looking at is pushed back on top of it with its own state, so Next still
 * recognises it as its own.
 *
 * The seeded entry carries Next's `_N` marker: without it Next's history patch
 * copies the visible page's route tree into the entry and Back would render
 * that page under the wrong URL. With it Next does not own the entry, so
 * returning to it reloads the page at `homePath`.
 *
 * Only for an entry nothing in the app sits behind; otherwise a no-op.
 */
export function seedHomeBelow(homePath: string): boolean {
  if (typeof window === "undefined" || canGoBackInApp()) return false;
  const history = window.history;
  const here = window.location.pathname + window.location.search + window.location.hash;
  const state = history.state;
  history.replaceState({ _N: true }, "", homePath);
  history.pushState(state ?? {}, "", here);
  return true;
}

/**
 * Step back to the bottom of the in-app run when it is `homePath`. Returns
 * false when it is not (or there is nothing behind), leaving navigation to
 * the caller.
 */
export function goHome(homePath: string): boolean {
  if (typeof window === "undefined") return false;
  const depth = depthOf(window.history.state);
  if (depth === 0 || baseOf(window.history.state) !== homePath) return false;
  window.history.go(-depth);
  return true;
}
