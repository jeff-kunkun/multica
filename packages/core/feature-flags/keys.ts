export const COMPOSIO_MCP_APPS_FLAG = "composio_mcp_apps";
export const PLUGINS_V1_FLAG = "plugins_v1";
export const BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG =
  "billing_workspace_subscriptions";
export const PROJECT_WORKFLOWS_V1_FLAG = "project_workflows_v1";
// DENE-2667 PR 1: shows when the watchdog reminded the project group about a
// live but silent run. Only on in office test builds.
export const OFFICE_WATCHDOG_GROUP_REMINDER_FLAG = "office_watchdog_group_reminder";
// DENE-2667 PR 2: offers "interrupt" for running turns that can stop their
// current step, as the default. Only on in office test builds.
export const OFFICE_INTERRUPT_STEER_FLAG = "office_interrupt_steer";
// DENE-2667 PR 3: the run row says when a 长任务 run has no idle limit. Desktop
// test builds with it also run their daemon under the office idle policy.
export const OFFICE_IDLE_STOP_FLAG = "office_idle_stop";
// DENE-2670 (DENE-2667 PR 5): "plan first" switch on new issues and the plan
// card with approve / change-it buttons. Only on in office test builds.
export const OFFICE_PLAN_FIRST_FLAG = "office_plan_first";
// DENE-2667 PR 4: running issue runs show their live status (command in flight,
// last output line, quiet time). Desktop test builds with it also have their
// daemon report it.
export const OFFICE_LIVE_STATUS_FLAG = "office_live_status";

// DENE-2667 PR 6: a comment's "create sub-issue" opens the manual form with
// the comment's first line as title and an "after" order. Only on in office
// test builds.
export const OFFICE_SPLIT_FROM_COMMENT_FLAG = "office_split_from_comment";

// DENE-2667 PR 11: each machine's default working directory field on the
// runtimes page. Only on in office test builds.
export const OFFICE_DEFAULT_DIR_FLAG = "office_default_dir";

// DENE-2667 PR 12: a project picks one folder per machine and the UI warns when
// two runs share a folder; project creation stops offering repositories and
// git init. Only on in office test builds.
export const OFFICE_PROJECT_FOLDER_FLAG = "office_project_folder";
