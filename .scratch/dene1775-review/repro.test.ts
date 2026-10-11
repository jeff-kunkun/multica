import { expect, it } from "vitest";
import { useTabStore } from "./tab-store";
it("project title sync converges after renaming to an 81-character title", () => {
  const store = useTabStore.getState();
  store.reset(); store.switchWorkspace("acme");
  const id = store.addTab("/acme/projects/p1", "Project");
  store.syncTabProjects("acme", { [id]: "p1" }, { p1: "Short" });
  const title = "a".repeat(81);
  store.syncTabProjects("acme", { [id]: "p1" }, { p1: title });
  const before = useTabStore.getState().byWorkspace.acme;
  store.syncTabProjects("acme", { [id]: "p1" }, { p1: title });
  const after = useTabStore.getState().byWorkspace.acme;
  console.log(JSON.stringify({nameLength: after.groups[0].name.length, sameWorkspace: before === after, sameTabs: before.tabs === after.tabs}));
  expect(after).toBe(before);
});
