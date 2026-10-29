/**
 * Public surface for @multica/core/feature-flags.
 *
 * Keep this list minimal — every new export becomes a contract we have to
 * preserve across the monorepo. Add to it only when a real caller appears.
 */

export type {
  Decision,
  EvalContext,
  PercentRollout,
  Provider,
  Reason,
  Rule,
} from "./types";

export { FeatureFlagService } from "./service";
export { StaticProvider } from "./static-provider";
export { ChainProvider } from "./chain-provider";
export {
  BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG,
  COMPOSIO_MCP_APPS_FLAG,
  OFFICE_WATCHDOG_GROUP_REMINDER_FLAG,
  OFFICE_INTERRUPT_STEER_FLAG,
  OFFICE_IDLE_STOP_FLAG,
  OFFICE_PLAN_FIRST_FLAG,
  OFFICE_LIVE_STATUS_FLAG,
  OFFICE_SPLIT_FROM_COMMENT_FLAG,
  OFFICE_DEFAULT_DIR_FLAG,
  OFFICE_PROJECT_FOLDER_FLAG,
  OFFICE_PARENT_GROUPS_FLAG,
  OFFICE_HIDE_WAKEUPS_FLAG,
  PLUGINS_V1_FLAG,
  PROJECT_WORKFLOWS_V1_FLAG,
} from "./keys";
export {
  FeatureFlagsProvider,
  useFeatureFlagService,
  useFlag,
  useVariant,
} from "./context";

// Hash helpers are exported for tests and for callers that want to share
// the bucketing logic without going through a Provider (rare; usually a
// red flag that the caller should be using the Service instead).
export { bucketFor, inPercent } from "./hash";
