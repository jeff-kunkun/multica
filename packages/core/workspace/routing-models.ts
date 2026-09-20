import { z } from "zod";

import { parseWithFallback } from "../api/schema";

/**
 * Model ids advertised by the routing gateway's OpenAI-compatible `/models`
 * endpoint. The endpoint never returns credentials or raw provider metadata.
 */
export interface RoutingModels {
  models: string[];
}

const RoutingModelsSchema = z.object({
  models: z.array(z.string()).default([]),
});

export const EMPTY_ROUTING_MODELS: RoutingModels = { models: [] };

/** Parse discovery defensively so a newer backend leaves manual entry usable. */
export function parseRoutingModels(raw: unknown): RoutingModels {
  const parsed = parseWithFallback(
    raw,
    RoutingModelsSchema,
    EMPTY_ROUTING_MODELS,
    { endpoint: "POST /api/workspaces/{id}/routing/models" },
  );
  return {
    models: parsed.models
      .map((model) => model.trim())
      .filter((model, index, all) => model !== "" && all.indexOf(model) === index),
  };
}
