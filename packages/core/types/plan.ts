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

export interface MeetingIssue {
  id: string;
  identifier: string;
  title: string;
  status: string;
  assignee_type: string | null;
  assignee_id: string | null;
  project_id: string | null;
  created_at: string;
  updated_at: string;
}

/** An issue opened in the meeting room, or any issue carrying a plan. */
export interface Meeting {
  issue: MeetingIssue;
  plan: Plan | null;
  steps_total: number;
  steps_done: number;
}
