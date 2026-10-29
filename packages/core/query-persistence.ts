import {
  dehydrate,
  hydrate,
  type DehydratedState,
  type Query,
  type QueryClient,
} from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";

// v2: v1 snapshots may hold a `Map` saved as `{}` (children-by-parents), which
// crashes task detail on restore; bumping drops them on the next start.
export const QUERY_CACHE_SCHEMA_VERSION = 2;
// The key stays `v1:` so the version check above finds and removes old snapshots.
const STORAGE_PREFIX = "multica_query_cache:v1:";

/** Query families that are either sensitive, highly volatile, or contain transient work. */
const EXCLUDED_SEGMENTS = new Set([
  "messages",
  "timeline",
  "attachments",
  "presence",
  "unread",
  "pending",
  "activeTasks",
  "tasks",
  "search",
  "billing",
]);

export interface PersistedQueryCacheEnvelope {
  schemaVersion: number;
  userId: string;
  state: DehydratedState;
}

function keyForUser(userId: string): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(userId)}`;
}

/**
 * Attachment queries hold signed URLs that expire and raw bytes (Blob) that
 * JSON turns into `{}`; a restored `{}` crashes `URL.createObjectURL`.
 */
const EXCLUDED_PREFIX = "attachment";

function isPersistableQuery(query: Query): boolean {
  const segments = query.queryKey.flatMap((part) =>
    typeof part === "string" ? [part] : [],
  );
  return (
    segments.length > 0 &&
    !segments.some((segment) => EXCLUDED_SEGMENTS.has(segment) || segment.startsWith(EXCLUDED_PREFIX))
  );
}

/**
 * Snapshot budget in UTF-16 code units. Browsers give an origin about 5M for
 * all of localStorage; an uncapped cache filled it and every other persisted
 * store then failed silently.
 */
export const QUERY_CACHE_MAX_CHARS = 2_000_000;

/** Shortest gap between two snapshot writes. */
export const QUERY_CACHE_WRITE_INTERVAL_MS = 3_000;

/** Marks a query the snapshot does not hold (not yet a success, or failed). */
const NOT_KEPT = Symbol("not kept");

/**
 * Only data JSON round-trips unchanged may be persisted. A `Map`, `Set`, `Blob`
 * or `Date` comes back as `{}` or a string, and the page that restores it
 * crashes on first render, before the background refetch can replace it.
 */
function isJsonSafe(value: unknown): boolean {
  if (value === null || value === undefined) return true;
  switch (typeof value) {
    case "string":
    case "boolean":
      return true;
    case "number":
      return Number.isFinite(value);
    case "object": {
      if (Array.isArray(value)) return value.every(isJsonSafe);
      const proto = Object.getPrototypeOf(value);
      if (proto !== Object.prototype && proto !== null) return false;
      return Object.values(value).every(isJsonSafe);
    }
    default:
      return false;
  }
}

export function isPersistedQueryKey(queryKey: readonly unknown[]): boolean {
  return isPersistableQuery({ queryKey } as Query);
}

export function clearPersistedQueryCache(
  storage: StorageAdapter,
  userId?: string | null,
): void {
  if (userId) {
    storage.removeItem(keyForUser(userId));
    return;
  }
  for (const key of storage.keys?.() ?? []) {
    if (key.startsWith(STORAGE_PREFIX)) storage.removeItem(key);
  }
}

export function createPersistedQueryCache(
  queryClient: QueryClient,
  storage: StorageAdapter,
  userId: string,
): () => void {
  const key = keyForUser(userId);
  try {
    const raw = storage.getItem(key);
    if (raw) {
      const envelope = JSON.parse(raw) as Partial<PersistedQueryCacheEnvelope>;
      if (envelope.schemaVersion === QUERY_CACHE_SCHEMA_VERSION && envelope.userId === userId && envelope.state) {
        // Drop entries a newer build no longer persists, so a snapshot an
        // older build wrote (e.g. a Blob saved as `{}`) cannot crash startup.
        hydrate(queryClient, {
          ...envelope.state,
          queries: (envelope.state.queries ?? []).filter((q) => isPersistedQueryKey(q.queryKey)),
        });
        // A restored snapshot is useful for the first paint, but must be
        // checked in the background even though the global client uses an
        // infinite stale time.
        void queryClient.invalidateQueries({
          predicate: (query) => isPersistableQuery(query),
          // Refetch active observers immediately. Inactive restored queries
          // remain stale and will refresh when their page mounts.
          refetchType: "active",
        });
      } else {
        storage.removeItem(key);
      }
    }
  } catch {
    storage.removeItem(key);
  }

  // Each write dehydrates, checks and stringifies the whole cache and then
  // stores it synchronously, so it runs at most once per interval, and only
  // when what the snapshot keeps has changed: a query's data while it is a
  // success, nothing otherwise. New data, a success turning into an error or
  // a reset, and the removal of a kept query all count. A poll or refetch
  // that returns the same data keeps the same reference (structural sharing)
  // and writes nothing (DENE-2756).
  const kept = new WeakMap<Query, unknown>();
  const keptNow = (query: Query) => (query.state.status === "success" ? query.state.data : NOT_KEPT);
  const keptBefore = (query: Query) => (kept.has(query) ? kept.get(query) : NOT_KEPT);
  // Queries restored above are already in the snapshot.
  for (const query of queryClient.getQueryCache().getAll()) {
    if (isPersistableQuery(query)) kept.set(query, keptNow(query));
  }
  let writeTimer: ReturnType<typeof setTimeout> | undefined;
  const write = () => {
    writeTimer = undefined;
    try {
      const state = dehydrate(queryClient, {
        shouldDehydrateQuery: (query) =>
          query.state.status === "success" && isPersistableQuery(query) && isJsonSafe(query.state.data),
      });
      storage.setItem(key, serializeWithinBudget(userId, state));
    } catch {
      // Storage is best effort (private mode/quota must never break the app).
      // Drop the stale snapshot so it does not keep holding the quota.
      try {
        storage.removeItem(key);
      } catch {
        // Nothing more to free.
      }
    }
  };
  const schedule = () => {
    writeTimer ??= setTimeout(write, QUERY_CACHE_WRITE_INTERVAL_MS);
  };
  const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
    const query = event.query;
    if (!isPersistableQuery(query)) return;
    if (event.type === "removed") {
      if (keptBefore(query) !== NOT_KEPT) schedule();
      kept.delete(query);
      return;
    }
    if (event.type !== "added" && event.type !== "updated") return;
    const now = keptNow(query);
    if (now === keptBefore(query)) return;
    kept.set(query, now);
    schedule();
  });

  // A pending write lands before the window hides or the page goes away, so
  // the throttle never costs the next start its snapshot.
  const flush = () => {
    if (!writeTimer) return;
    clearTimeout(writeTimer);
    write();
  };
  const onVisibilityChange = () => {
    if (document.visibilityState === "hidden") flush();
  };
  const hasPage = typeof window !== "undefined" && typeof document !== "undefined";
  if (hasPage) {
    window.addEventListener("pagehide", flush);
    document.addEventListener("visibilitychange", onVisibilityChange);
  }

  return () => {
    unsubscribe();
    if (writeTimer) clearTimeout(writeTimer);
    writeTimer = undefined;
    if (hasPage) {
      window.removeEventListener("pagehide", flush);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    }
  };
}

/** Most recently updated queries first, until the snapshot hits the budget. */
function serializeWithinBudget(userId: string, state: DehydratedState): string {
  const head = JSON.stringify({ schemaVersion: QUERY_CACHE_SCHEMA_VERSION, userId, state: { mutations: [], queries: [] } });
  const prefix = head.slice(0, -"]}}".length);
  const kept: string[] = [];
  let size = head.length;
  const queries = [...state.queries].sort((a, b) => b.state.dataUpdatedAt - a.state.dataUpdatedAt);
  for (const query of queries) {
    const part = JSON.stringify(query);
    if (size + part.length + 1 > QUERY_CACHE_MAX_CHARS) continue;
    kept.push(part);
    size += part.length + 1;
  }
  return `${prefix}${kept.join(",")}]}}`;
}

export function queryCacheStoragePrefix(): string {
  return STORAGE_PREFIX;
}
