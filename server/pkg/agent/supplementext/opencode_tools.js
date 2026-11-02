// Multica running-tool plugin for `opencode run` (OpenCode 1.x), loaded
// through OPENCODE_CONFIG_CONTENT. `opencode run --format json` prints a tool
// only once it finished, so a long command looks exactly like a silent hang.
// This plugin keeps one file per running tool call in MULTICA_TOOL_ACTIVITY_DIR
// and removes it when the call completes or fails (see opencode_tools.go).
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

// Only the fields that name what the call does; a write's file body stays out.
const SUMMARY_KEYS = ["command", "cmd", "filePath", "file_path", "path", "pattern", "include", "url", "query", "description"];
const MAX_FIELD = 500;

export const MulticaToolActivityPlugin = async () => {
  const dir = process.env.MULTICA_TOOL_ACTIVITY_DIR;
  if (!dir) return {};
  const running = new Set();
  const fileFor = (callID) => path.join(dir, crypto.createHash("sha256").update(callID).digest("hex") + ".json");

  return {
    event: async ({ event }) => {
      if (event?.type !== "message.part.updated") return;
      const part = event.properties?.part;
      if (part?.type !== "tool" || !part.callID) return;
      const status = part.state?.status;
      if (status === "running") {
        // A running bash part is re-published as its output grows.
        if (running.has(part.callID)) return;
        running.add(part.callID);
        const input = {};
        for (const key of SUMMARY_KEYS) {
          const value = part.state.input?.[key];
          if (typeof value === "string" && value !== "") input[key] = value.slice(0, MAX_FIELD);
        }
        const body = JSON.stringify({
          call_id: part.callID,
          tool: part.tool || "",
          input,
          started_at: part.state.time?.start || Date.now(),
        });
        const file = fileFor(part.callID);
        try {
          fs.writeFileSync(file + ".tmp", body);
          fs.renameSync(file + ".tmp", file);
        } catch {}
      } else if (status === "completed" || status === "error") {
        running.delete(part.callID);
        try {
          fs.unlinkSync(fileFor(part.callID));
        } catch {}
      }
    },
  };
};
