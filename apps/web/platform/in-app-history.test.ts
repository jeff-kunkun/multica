import { afterEach, describe, expect, it, vi } from "vitest";
import { canGoBackInApp, goHome, installInAppHistoryDepth, seedHomeBelow } from "./in-app-history";

const win = window as unknown as { navigation?: unknown };

function withNavigationApi(navigation: unknown) {
  win.navigation = navigation;
}

afterEach(() => {
  delete win.navigation;
  popTo(null);
});

// Installed once for the file, like the adapter does for the page.
installInAppHistoryDepth();
installInAppHistoryDepth();

// jsdom has no real traversal: write the entry the browser would restore
// with the unwrapped method, so the stamp under test is the only one.
function popTo(state: unknown) {
  const replace = Object.getPrototypeOf(window.history).replaceState as History["replaceState"];
  replace.call(window.history, state, "");
}

function resetHistory() {
  popTo(null);
}

describe("in-app history depth (no Navigation API)", () => {
  it("a cold entry has nothing in the app behind it", () => {
    resetHistory();
    expect(canGoBackInApp()).toBe(false);
  });

  it("chat → issue: the issue entry can step back to the chat", () => {
    resetHistory();
    window.history.pushState({ __NA: true }, "", "/acme/chat/s1");
    window.history.pushState({ __NA: true }, "", "/acme/issues/i1");

    expect(window.history.state).toMatchObject({ __NA: true, __multicaDepth: 2 });
    expect(canGoBackInApp()).toBe(true);
  });

  it("an issue opened cold stays without history after the app redraws it", () => {
    resetHistory();
    window.history.replaceState({ __NA: true }, "", "/acme/issues/i1");

    expect(canGoBackInApp()).toBe(false);
  });

  it("a replace keeps the depth of the entry it rewrites", () => {
    resetHistory();
    window.history.pushState({ __NA: true }, "", "/acme/chat");
    window.history.replaceState({ __NA: true }, "", "/acme/chat/s1");

    expect(window.history.state).toMatchObject({ __multicaDepth: 1 });
  });

  it("reads the depth of the entry the browser restores", () => {
    popTo({ __NA: true, __multicaDepth: 1 });
    expect(canGoBackInApp()).toBe(true);
    popTo({ __NA: true });
    expect(canGoBackInApp()).toBe(false);
    popTo({ __multicaDepth: "3" });
    expect(canGoBackInApp()).toBe(false);
  });

  it("does not mutate the caller's state object", () => {
    resetHistory();
    const data = { __NA: true };
    window.history.pushState(data, "", "/acme/inbox");

    expect(data).toEqual({ __NA: true });
  });

  it("defers to the Navigation API when the browser has one", () => {
    resetHistory();
    window.history.pushState({ __NA: true }, "", "/acme/issues/i1");
    withNavigationApi({ canGoBack: false });

    expect(canGoBackInApp()).toBe(false);
  });
});

describe("canGoBackInApp", () => {
  it("reports the Navigation API's answer when the browser has one", () => {
    withNavigationApi({ canGoBack: true });

    expect(canGoBackInApp()).toBe(true);
  });

  // Not "nothing was pushed yet" — arriving from an external site leaves the
  // same-origin run empty, which is the whole reason we ask the browser.
  it("reports false when the Navigation API says there is no same-origin entry", () => {
    withNavigationApi({ canGoBack: false });

    expect(canGoBackInApp()).toBe(false);
  });

  // jsdom ships no `window.navigation`. Callers then take their fallback path,
  // which is what these browsers did before any of this existed — deliberately
  // no guessing, because a wrong `true` walks the user out of the app.
  it("reports false when the browser has no Navigation API", () => {
    expect(canGoBackInApp()).toBe(false);
  });

  it("reports false for a `navigation` global that is not the Navigation API", () => {
    withNavigationApi({ somethingElse: true });

    expect(canGoBackInApp()).toBe(false);
  });

  it("reports false for a non-boolean canGoBack", () => {
    withNavigationApi({ canGoBack: "yes" });

    expect(canGoBackInApp()).toBe(false);
  });
});

describe("seedHomeBelow", () => {
  it("slips the home page under a page opened cold", () => {
    resetHistory();
    window.history.replaceState({ __NA: true, tree: "t" }, "", "/acme/issues/i1");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});

    expect(seedHomeBelow("/acme/chat")).toBe(true);

    // The visible page keeps Next's state and sits one deep, over the home.
    expect(window.location.pathname).toBe("/acme/issues/i1");
    expect(window.history.state).toMatchObject({
      __NA: true,
      tree: "t",
      __multicaDepth: 1,
      __multicaBase: "/acme/chat",
    });
    expect(canGoBackInApp()).toBe(true);
    go.mockRestore();
  });

  it("does nothing when the app already has a page behind", () => {
    resetHistory();
    window.history.pushState({ __NA: true }, "", "/acme/chat");
    window.history.pushState({ __NA: true }, "", "/acme/issues/i1");

    expect(seedHomeBelow("/acme/chat")).toBe(false);
    expect(window.history.state).toMatchObject({ __multicaDepth: 2 });
  });
});

describe("goHome", () => {
  it("steps back to the bottom of the run when it is the home page", () => {
    resetHistory();
    window.history.replaceState({ __NA: true }, "", "/acme/chat");
    window.history.pushState({ __NA: true }, "", "/acme/inbox");
    window.history.pushState({ __NA: true }, "", "/acme/issues/i1");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});

    expect(goHome("/acme/chat")).toBe(true);
    expect(go).toHaveBeenCalledWith(-2);
    go.mockRestore();
  });

  it("leaves it to the caller when the bottom is another page or there is none", () => {
    resetHistory();
    window.history.replaceState({ __NA: true }, "", "/acme/issues");
    window.history.pushState({ __NA: true }, "", "/acme/inbox");
    const go = vi.spyOn(window.history, "go").mockImplementation(() => {});

    expect(goHome("/acme/chat")).toBe(false);
    window.history.go(0);
    resetHistory();
    expect(goHome("/acme/chat")).toBe(false);
    expect(go).toHaveBeenCalledTimes(1);
    go.mockRestore();
  });

  it("does not take another workspace's chat for home", () => {
    resetHistory();
    window.history.replaceState({ __NA: true }, "", "/old/chat");
    window.history.pushState({ __NA: true }, "", "/new/inbox");

    expect(goHome("/new/chat")).toBe(false);
  });
});
