import type { IssueViewVisibility } from "../api/schemas";

const SHARING_CHOICES = ["private", "workspace"] as const satisfies readonly IssueViewVisibility[];
const PROJECT_SHARING_CHOICES = [
  "private",
  "workspace",
  "project",
] as const satisfies readonly IssueViewVisibility[];

/** Coerce a lenient API string into the three known sharing values. */
export function parseIssueViewVisibility(
  value: string | null | undefined,
): IssueViewVisibility {
  if (value === "workspace" || value === "project") return value;
  return "private";
}

/**
 * Sharing options the user may pick for a view of this scope. `my` views
 * are forced private (the dialog hides the control). The third `project`
 * choice is only legal on project-scoped views.
 */
export function issueViewSharingChoices(
  scopeType: string,
): readonly IssueViewVisibility[] {
  if (scopeType === "project") return PROJECT_SHARING_CHOICES;
  return SHARING_CHOICES;
}
