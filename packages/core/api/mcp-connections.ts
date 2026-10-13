import { z } from "zod";
import { parseWithFallback } from "./schema";

export const McpConnectionSchema = z
  .object({
    id: z.string().min(1),
    name: z.string(),
    bot_id: z.string(),
    expires_at: z.string(),
    created_at: z.string(),
    last_used_at: z.string().nullish(),
    revoked: z.boolean(),
  })
  .transform((row) => ({
    id: row.id,
    name: row.name,
    botId: row.bot_id,
    expiresAt: row.expires_at,
    createdAt: row.created_at,
    lastUsedAt: row.last_used_at ?? null,
    revoked: row.revoked,
  }));

export const McpConnectionGrantSchema = z
  .object({
    id: z.string().min(1),
    code: z.string().min(1),
    expires_at: z.string(),
    endpoint: z
      .string()
      .url()
      .refine((value) => /^https?:\/\//.test(value)),
    exchange_url: z
      .string()
      .url()
      .refine((value) => /^https?:\/\//.test(value)),
  })
  .transform((row) => ({
    id: row.id,
    code: row.code,
    expiresAt: row.expires_at,
    endpoint: row.endpoint,
    exchangeUrl: row.exchange_url,
  }));

export type McpConnection = z.output<typeof McpConnectionSchema>;
export type McpConnectionGrant = z.output<typeof McpConnectionGrantSchema>;

export function parseMcpConnections(raw: unknown): McpConnection[] {
  const result = parseWithFallback<McpConnection[] | null>(
    raw,
    z.array(McpConnectionSchema),
    null,
    { endpoint: "mcp-connections" },
  );
  if (!result) throw new Error("Unable to read MCP connections");
  return result;
}

export function parseMcpConnectionGrant(raw: unknown): McpConnectionGrant {
  const result = parseWithFallback<McpConnectionGrant | null>(
    raw,
    McpConnectionGrantSchema,
    null,
    { endpoint: "mcp-connections/create" },
  );
  if (!result) throw new Error("Unable to read MCP authorization");
  return result;
}

const OfficeBotRouteSchema = z
  .object({
    project_id: z.string().nullable(),
    bot_id: z.string(),
    channel_ref: z.string(),
  })
  .transform((row) => ({
    projectId: row.project_id,
    botId: row.bot_id,
    channelRef: row.channel_ref,
  }));
export type OfficeBotRoute = z.output<typeof OfficeBotRouteSchema>;
export function parseOfficeBotRoutes(raw: unknown): OfficeBotRoute[] {
  const result = parseWithFallback<OfficeBotRoute[] | null>(
    raw,
    z.array(OfficeBotRouteSchema),
    null,
    { endpoint: "office-bot-routes" },
  );
  if (!result) throw new Error("Unable to read notification routes");
  return result;
}
export function parseOfficeBotRoute(raw: unknown): OfficeBotRoute {
  const result = parseWithFallback<OfficeBotRoute | null>(
    raw,
    OfficeBotRouteSchema,
    null,
    { endpoint: "office-bot-routes/save" },
  );
  if (!result) throw new Error("Unable to read notification route");
  return result;
}
