/** One step of a plan; approval turns it into a child issue (`issue_id`). */
export interface PlanStep {
  title: string;
  assignee_type: string;
  assignee_id: string;
  issue_id: string | null;
}

/** Plan status: "proposed" | "approved" | "superseded" (unknown values pass through). */
export interface Plan {
  id: string;
  issue_id: string;
  version: number;
  status: string;
  author_type: string;
  author_id: string;
  goal: string;
  out_of_scope: string;
  steps: PlanStep[];
  done_when: string;
  /** Project approval files the work in; null = general (no project). */
  project_id: string | null;
  comment_id: string | null;
  approved_by: string | null;
  approved_at: string | null;
  created_at: string;
}

export interface IssuePlans {
  /** Newest version first. */
  plans: Plan[];
  can_approve: boolean;
}

export interface PlanApprover {
  user_id: string;
  name: string;
  granted_by: string;
  created_at: string;
}
