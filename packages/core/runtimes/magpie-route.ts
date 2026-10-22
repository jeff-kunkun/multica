import { z } from "zod";
import { parseWithFallback } from "../api/schema";

export interface MagpieModelRef {
  id: string;
  name: string;
}

/** One of the magpie groups on a runtime's machine. */
export interface MagpieGroupOption {
  name: string;
  models: MagpieModelRef[];
}

/**
 * The magpie route a daemon reports into a runtime's `metadata.magpie`: the
 * routing group magpie serves this CLI through and its models in order (the
 * first is the primary, the rest take over in turn). An empty `group` means
 * magpie is installed on the machine but does not route this CLI. `groups` is
 * every group on the machine, empty from daemons that predate the list.
 */
export interface MagpieRoute {
  group: string;
  models: MagpieModelRef[];
  groups: MagpieGroupOption[];
}

const MagpieModelSchema = z.object({
  id: z.string().min(1),
  name: z.string().optional(),
});

const MagpieGroupOptionSchema = z.object({
  name: z.string().min(1),
  models: z.array(MagpieModelSchema).min(1),
});

const MagpieRouteSchema = z
  .object({
    group: z.string(),
    models: z.array(MagpieModelSchema),
    groups: z.array(z.unknown()).optional(),
  })
  .refine((r) => r.group === "" || r.models.length > 0);

function modelRefs(models: z.infer<typeof MagpieModelSchema>[]): MagpieModelRef[] {
  return models.map((m) => ({
    id: m.id,
    name: m.name || m.id.slice(m.id.lastIndexOf("/") + 1),
  }));
}

/**
 * Read a runtime's magpie route. `null` — show nothing — when the daemon
 * predates the report, magpie is not installed on its machine, or the value
 * has a shape this client does not know. A malformed group in the list is
 * dropped on its own.
 */
export function readRuntimeMagpieRoute(
  metadata: Record<string, unknown> | null | undefined,
): MagpieRoute | null {
  const raw = metadata?.magpie;
  if (raw === undefined || raw === null) return null;
  const parsed = parseWithFallback<z.infer<typeof MagpieRouteSchema> | null>(
    raw,
    MagpieRouteSchema,
    null,
    { endpoint: "runtime.metadata.magpie" },
  );
  if (!parsed) return null;
  const groups: MagpieGroupOption[] = [];
  for (const entry of parsed.groups ?? []) {
    const group = MagpieGroupOptionSchema.safeParse(entry);
    if (group.success) {
      groups.push({ name: group.data.name, models: modelRefs(group.data.models) });
    }
  }
  return {
    group: parsed.group,
    models: parsed.group ? modelRefs(parsed.models) : [],
    groups,
  };
}

/**
 * The runtime's magpie route when magpie serves its CLI through a group, else
 * `null`. With the workspace switch on, agents on such a runtime are magpie
 * agents; every other agent is native and shows no magpie controls. Matches
 * the server's `runtimeHasMagpieRoute`.
 */
export function readRuntimeMagpieGroupRoute(
  metadata: Record<string, unknown> | null | undefined,
): MagpieRoute | null {
  const route = readRuntimeMagpieRoute(metadata);
  return route && route.group !== "" ? route : null;
}

/**
 * How each CLI is pinned to a magpie group: the `--model` value prefix magpie's
 * provider gives its groups in that CLI. A CLI missing here cannot be pinned
 * from the office.
 */
const MAGPIE_GROUP_MODEL_PREFIX: Partial<Record<string, string>> = {
  opencode: "magpie/group/",
};

/** The issue metadata key that picks which of the assignee's groups a run uses. */
export const ISSUE_MAGPIE_GROUP_KEY = "magpie_group";

export function canPinMagpieGroup(provider: string | null | undefined): boolean {
  return !!provider && MAGPIE_GROUP_MODEL_PREFIX[provider] !== undefined;
}

/** The `--model` value in custom args and where it sits, if any. */
function findModelArg(args: string[]): { start: number; count: number; value: string } | null {
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]!;
    if (arg === "--model" || arg === "-m") {
      return { start: i, count: 2, value: args[i + 1] ?? "" };
    }
    if (arg.startsWith("--model=")) {
      return { start: i, count: 1, value: arg.slice("--model=".length) };
    }
  }
  return null;
}

/**
 * The magpie group an agent's custom args pin it to, `null` when it follows
 * its CLI's own group.
 */
export function agentMagpieGroup(
  provider: string | null | undefined,
  customArgs: string[],
): string | null {
  const prefix = provider ? MAGPIE_GROUP_MODEL_PREFIX[provider] : undefined;
  if (!prefix) return null;
  const model = findModelArg(customArgs);
  if (!model?.value.startsWith(prefix)) return null;
  return model.value.slice(prefix.length) || null;
}

/**
 * The route an agent actually runs on: its pinned group when it has one,
 * else its runtime's. A pinned group the machine no longer lists shows with
 * no models.
 */
export function agentMagpieRoute(
  route: MagpieRoute | null,
  provider: string | null | undefined,
  customArgs: string[],
): MagpieRoute | null {
  if (!route) return null;
  const pinned = agentMagpieGroup(provider, customArgs);
  if (!pinned || pinned === route.group) return route;
  const option = route.groups.find((g) => g.name === pinned);
  return { group: pinned, models: option?.models ?? [], groups: route.groups };
}

/**
 * The magpie groups an agent can run on (`runtime_config.magpie_groups`),
 * its default first. Empty when it has none: it then runs as its custom args
 * and CLI say.
 */
export function agentMagpieGroups(runtimeConfig: Record<string, unknown> | null | undefined): string[] {
  const raw = runtimeConfig?.magpie_groups;
  if (!Array.isArray(raw)) return [];
  const groups: string[] = [];
  for (const g of raw) {
    if (typeof g === "string" && g.trim() && !groups.includes(g.trim())) groups.push(g.trim());
  }
  return groups;
}

/** runtime_config with the agent's groups replaced, other keys kept. */
export function withAgentMagpieGroups(
  runtimeConfig: Record<string, unknown> | null | undefined,
  groups: string[],
): Record<string, unknown> {
  const next = { ...(runtimeConfig ?? {}) };
  if (groups.length > 0) next.magpie_groups = groups;
  else delete next.magpie_groups;
  return next;
}

/**
 * Every route an agent can run on, its default first: one per group it
 * lists, or else the single route its custom args and CLI give it. A group
 * the machine no longer lists shows with no models.
 */
export function agentMagpieRoutes(
  route: MagpieRoute | null,
  provider: string | null | undefined,
  agent: { custom_args: string[]; runtime_config: Record<string, unknown> | null | undefined },
): MagpieRoute[] {
  if (!route) return [];
  const groups = canPinMagpieGroup(provider) ? agentMagpieGroups(agent.runtime_config) : [];
  if (groups.length === 0) {
    const single = agentMagpieRoute(route, provider, agent.custom_args);
    return single ? [single] : [];
  }
  return groups.map((group) => ({
    group,
    models: route.groups.find((g) => g.name === group)?.models ?? [],
    groups: route.groups,
  }));
}
