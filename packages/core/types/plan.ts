/** Plan first (DENE-2670): one version of the plan an agent proposed. */
export interface IssuePlan {
  id: string;
  issue_id: string;
  version: number;
  /** proposed | superseded | approved */
  status: string;
  author_type: string;
  author_id: string;
  goal: string;
  out_of_scope: string;
  steps: { title: string }[];
  done_when: string;
  comment_id: string | null;
  approved_by: string | null;
  approved_at: string | null;
  changes_requested_by: string | null;
  changes_requested_at: string | null;
  created_at: string;
}

/**
 * An approve or send-back answer: the plan, and whom the reply under its card
 * woke. none_woken means the agent was not reached; approving again retries.
 */
export interface IssuePlanAnswer extends IssuePlan {
  wake?: { none_woken: boolean };
}

/** GET /api/issues/:id/plans. gated && !approved is the only blocking state. */
export interface IssuePlansResponse {
  gated: boolean;
  approved: boolean;
  can_approve: boolean;
  plans: IssuePlan[];
}
