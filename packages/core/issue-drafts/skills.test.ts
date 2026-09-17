// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  DEFAULT_ISSUE_DRAFT_SKILLS,
  ISSUE_DRAFT_SKILLS,
  isIssueDraftSkillKey,
  readIssueDraftSkills,
} from "./skills";

/**
 * The client half of the alignment skill set is a whitelist and a decoder, and
 * both are load-bearing: the toggle renders one checkbox per entry here, and a
 * decoded set is what those checkboxes are checked from. A key the client does
 * not know must never be offered, and a recorded set the client cannot fully
 * name must still show the part it can.
 */
describe("ISSUE_DRAFT_SKILLS", () => {
  it("lists exactly the skills the server registry ships", () => {
    // Mirrors issueDraftSkillRegistry in server/internal/handler/issue_draft_policy.go.
    // A key the server accepts but this list omits is a skill the toggle can
    // never turn on; one this list carries and the server does not is a
    // checkbox that always 400s.
    expect(ISSUE_DRAFT_SKILLS).toEqual(["grill", "wayfinder", "frontend"]);
  });

  it("defaults to the requirement interview alone", () => {
    expect(DEFAULT_ISSUE_DRAFT_SKILLS).toEqual(["grill"]);
    // The default has to be offerable, or the panel opens showing a skill it
    // cannot render.
    for (const key of DEFAULT_ISSUE_DRAFT_SKILLS) {
      expect(isIssueDraftSkillKey(key)).toBe(true);
    }
  });

  it("recognises only its own keys", () => {
    for (const key of ISSUE_DRAFT_SKILLS) {
      expect(isIssueDraftSkillKey(key)).toBe(true);
    }
    // The retired single-policy keys and near-misses are not skills.
    for (const value of ["question", "conversation", "grill-frontend-look", "", "GRILL", "front-end"]) {
      expect(isIssueDraftSkillKey(value)).toBe(false);
    }
  });
});

describe("readIssueDraftSkills", () => {
  it("prefers the list form, which is where the versions live", () => {
    expect(
      readIssueDraftSkills({
        key: "frontend+grill",
        skills: [{ key: "frontend" }, { key: "grill" }],
      }),
    ).toEqual(["frontend", "grill"]);
  });

  it("falls back to the joined key when the backend sends no list", () => {
    // Response drift, not a legacy backend: the record is still readable.
    expect(readIssueDraftSkills({ key: "grill+wayfinder" })).toEqual([
      "grill",
      "wayfinder",
    ]);
    // A single-key record from before the set existed decodes to one skill.
    expect(readIssueDraftSkills({ key: "frontend" })).toEqual(["frontend"]);
  });

  it("drops keys it cannot name instead of rendering a checkbox for them", () => {
    expect(
      readIssueDraftSkills({
        key: "cosmic+grill",
        skills: [{ key: "cosmic" }, { key: "grill" }],
      }),
    ).toEqual(["grill"]);
    // A retired key recorded on an old draft is not an enabled skill, and a
    // draft with no policy at all has an empty set — the "hide the control"
    // state, which readIssueDraftSkills must not confuse with "all on".
    expect(readIssueDraftSkills({ key: "conversation" })).toEqual([]);
    expect(readIssueDraftSkills({ key: "" })).toEqual([]);
  });
});
