import { describe, expect, it } from "vitest";
import { matchRoutes } from "react-router-dom";

import { appRoutes } from "./routes";

/**
 * Tab restore for an alignment conversation (DENE-342).
 *
 * A restored tab is replayed through the router as a plain URL, so what a
 * reopened desktop app shows for `/{slug}/issues/new/{draftId}` is decided
 * entirely by route matching: the creation flow, or an issue whose id is the
 * literal string `new`. React Router ranks by specificity rather than
 * declaration order, so the literal segment wins either way — the order
 * assertion below guards the invariant the route comment states, not the
 * match itself.
 *
 * Canonical coverage for the persisted-tab half of the same flow lives in
 * `stores/tab-store.test.ts` ("issue draft tabs survive restore").
 */
describe("issue draft route matching", () => {
  const draftId = "01a0aaae-bd66-76e3-8074-a660bf29473b";

  function deepestMatch(pathname: string) {
    const matches = matchRoutes(appRoutes, pathname);
    expect(matches, `no route matched ${pathname}`).toBeTruthy();
    return matches![matches!.length - 1];
  }

  it("routes a restored draft url to the alignment page, not to issue detail", () => {
    const match = deepestMatch(`/acme/issues/new/${draftId}`);

    expect(match.route.path).toBe("issues/new/:draftId");
    expect(match.params.draftId).toBe(draftId);
    // The failure mode this guards: `new` read as an issue identifier.
    expect(match.params.id).toBeUndefined();
  });

  it("titles the restored tab as the creation flow, never as the raw id segment", () => {
    const match = deepestMatch(`/acme/issues/new/${draftId}`);

    const title = (match.route.handle as { title?: string } | undefined)?.title;
    expect(title).toBe("Align Issue");
    expect(title).not.toBe("new");
  });

  it("still routes a real issue url to issue detail", () => {
    const match = deepestMatch("/acme/issues/MUL-123");

    expect(match.route.path).toBe("issues/:id");
    expect(match.params.id).toBe("MUL-123");
  });

  it("declares issues/new/:draftId above issues/:id", () => {
    const workspaceChildren = appRoutes
      .flatMap((route) => route.children ?? [])
      .flatMap((route) => route.children ?? []);
    const paths = workspaceChildren.map((route) => route.path);

    const draftIndex = paths.indexOf("issues/new/:draftId");
    const detailIndex = paths.indexOf("issues/:id");
    expect(draftIndex).toBeGreaterThanOrEqual(0);
    expect(detailIndex).toBeGreaterThanOrEqual(0);
    expect(draftIndex).toBeLessThan(detailIndex);
  });
});
