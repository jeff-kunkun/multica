/**
 * The client half of the alignment skills.
 *
 * The server owns the skills — their method prompts, their versions, and how a
 * set of them composes into the carrier's instructions
 * (server/internal/handler/issue_draft_policy.go). The client only ever chooses
 * between the keys it knows, which is why this list is short and explicit: the
 * toggle is rendered from it, so a key the client has never heard of is never
 * offered.
 *
 * A draft reports its own set in `policy` — the "+"-joined keys, the same join
 * for their versions, and a list form the UI restores checkboxes from. That is
 * the audit trail: a conversation keeps reporting what it ran after the registry
 * has moved on.
 *
 * Order is the order the toggle renders, and it is the order the server composes
 * the prompt in, so what the user reads top to bottom is what the carrier reads
 * first to last.
 */
export const ISSUE_DRAFT_SKILLS = [
  "grill",
  "wayfinder",
  "frontend",
] as const;

export type IssueDraftSkillKey = (typeof ISSUE_DRAFT_SKILLS)[number];

/**
 * Which skills a conversation opens with when the entry point does not say:
 * the requirement interview, which is what "align the requirement first" has
 * always meant. The other two are reached for on purpose — a decision map when
 * the route is still foggy, a prototype when the screen has to be seen.
 *
 * Exported as a value, not spelled at the call site: the modal's seeded state
 * and the server's default have to be the same set, or the panel would show one
 * thing and the conversation would run another.
 */
export const DEFAULT_ISSUE_DRAFT_SKILLS: readonly IssueDraftSkillKey[] = ["grill"];

/** Whether a server-supplied skill key is one this client can toggle. */
export function isIssueDraftSkillKey(value: string): value is IssueDraftSkillKey {
  return (ISSUE_DRAFT_SKILLS as readonly string[]).includes(value);
}

/**
 * The skill keys a recorded draft is running, read from the list form when the
 * backend sends it and from the joined `key` when it does not.
 *
 * The fallback is for response drift, not for an old backend in the ordinary
 * sense: the list form carries the versions, so a body that has it is preferred.
 * A body that only has `key` still answers "which skills", which is all the
 * toggle needs — an unrecognised key is dropped rather than rendered as an
 * option this client cannot name, and the caller shows the raw key beside the
 * toggle so the state is still visible.
 */
export function readIssueDraftSkills(policy: {
  key: string;
  skills?: { key: string }[];
}): IssueDraftSkillKey[] {
  if (policy.skills && policy.skills.length > 0) {
    return policy.skills
      .map((skill) => skill.key)
      .filter(isIssueDraftSkillKey);
  }
  if (!policy.key) return [];
  return policy.key.split("+").filter(isIssueDraftSkillKey);
}
