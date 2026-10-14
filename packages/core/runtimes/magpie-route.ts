import { z } from "zod";
import { parseWithFallback } from "../api/schema";

/**
 * The magpie route a daemon reports into a runtime's `metadata.magpie`: the
 * routing group magpie serves this CLI through and its models in order (the
 * first is the primary, the rest take over in turn). An empty `group` means
 * magpie is installed on the machine but does not route this CLI.
 */
export interface MagpieRoute {
  group: string;
  models: { id: string; name: string }[];
}

const MagpieRouteSchema = z
  .object({
    group: z.string(),
    models: z.array(
      z.object({ id: z.string().min(1), name: z.string().optional() }),
    ),
  })
  .refine((r) => r.group === "" || r.models.length > 0);

/**
 * Read a runtime's magpie route. `null` — show nothing — when the daemon
 * predates the report, magpie is not installed on its machine, or the value
 * has a shape this client does not know.
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
  return {
    group: parsed.group,
    models: parsed.group
      ? parsed.models.map((m) => ({
          id: m.id,
          name: m.name || m.id.slice(m.id.lastIndexOf("/") + 1),
        }))
      : [],
  };
}
