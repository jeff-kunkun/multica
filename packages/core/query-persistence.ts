import {
  dehydrate,
  hydrate,
  type DehydratedState,
  type Query,
  type QueryClient,
} from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";

export const QUERY_CACHE_SCHEMA_VERSION = 1;
const STORAGE_PREFIX = "multica_query_cache:v1:";

/**
 * Query families that are either sensitive, highly volatile, or contain
 * transient work. Any segment starting with "attachment" is excluded too:
 * those hold raw bytes or short-lived signed URLs.
 */
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
  "routing-health",
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

function isExcludedSegment(segment: string): boolean {
  return EXCLUDED_SEGMENTS.has(segment) || segment.startsWith("attachment");
}

function isPersistableQuery(query: Query): boolean {
  const segments = query.queryKey.flatMap((part) =>
    typeof part === "string" ? [part] : [],
  );
  return segments.length > 0 && !segments.some(isExcludedSegment);
}

// JSON turns a Blob or ArrayBuffer into `{}`; a restored `{}` then crashes
// whoever expects bytes (e.g. URL.createObjectURL).
function containsBinary(value: unknown, depth = 0): boolean {
  if (value === null || typeof value !== "object" || depth > 2) return false;
  if (
    (typeof Blob !== "undefined" && value instanceof Blob) ||
    value instanceof ArrayBuffer ||
    ArrayBuffer.isView(value)
  ) {
    return true;
  }
  const children = Array.isArray(value) ? value : Object.values(value);
  return children.some((child) => containsBinary(child, depth + 1));
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
        // Drop entries the current rules would not persist, so a cache an
        // older build wrote (e.g. attachment bytes stored as `{}`) heals on
        // upgrade instead of crashing every launch.
        hydrate(queryClient, {
          ...envelope.state,
          queries: (envelope.state.queries ?? []).filter((query) =>
            isPersistedQueryKey(query.queryKey),
          ),
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

  let writeTimer: ReturnType<typeof setTimeout> | undefined;
  const unsubscribe = queryClient.getQueryCache().subscribe(() => {
    if (writeTimer) clearTimeout(writeTimer);
    writeTimer = setTimeout(() => {
      try {
        const state = dehydrate(queryClient, {
          shouldDehydrateQuery: (query) => query.state.status === "success" &&
            isPersistableQuery(query) &&
            !containsBinary(query.state.data),
        });
        const envelope: PersistedQueryCacheEnvelope = {
          schemaVersion: QUERY_CACHE_SCHEMA_VERSION,
          userId,
          state,
        };
        storage.setItem(key, JSON.stringify(envelope));
      } catch {
        // Storage is best effort (private mode/quota must never break the app).
      }
    }, 50);
  });

  return () => {
    unsubscribe();
    if (writeTimer) clearTimeout(writeTimer);
  };
}

export function queryCacheStoragePrefix(): string {
  return STORAGE_PREFIX;
}
