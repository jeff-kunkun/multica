function createMemoryStorage(): Storage {
  const values = new Map<string, string>();

  return {
    get length() {
      return values.size;
    },
    clear: () => values.clear(),
    getItem: (key: string) => values.get(key) ?? null,
    key: (index: number) => Array.from(values.keys())[index] ?? null,
    removeItem: (key: string) => {
      values.delete(key);
    },
    setItem: (key: string, value: string) => {
      values.set(key, value);
    },
  };
}

// Node ships its own Web Storage globals, and they shadow the jsdom
// implementation Vitest installs for the suites that opt into jsdom with
// `@vitest-environment jsdom`. Without `--localstorage-file` reading that
// global yields `undefined`, so every bare `localStorage` reference throws —
// in a test, in `platform/storage.ts`, and inside zustand's persist
// middleware. Install an in-memory Storage on both globals so the suite does
// not depend on which Node patch the developer happens to run.
//
// Pure-logic suites opt out of jsdom and share this file, so there is no DOM to
// patch there — bail out rather than guard each stub. packages/views and
// apps/web carry the same patch in their own setup files.
if (
  typeof window !== "undefined" &&
  typeof globalThis.localStorage?.clear !== "function"
) {
  const storage = createMemoryStorage();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: storage,
  });
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: storage,
  });
}
