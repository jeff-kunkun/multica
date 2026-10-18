import type { OfficeLatch, OfficeStopReceipt, OfficeStopStep } from "../../shared/office-screen";

// The stop receipt is built from what each process says it read, never from
// what the app wrote: the broker's /health and the daemon's /health
// stop_latch. A process too old to report either is "outdated" — the button
// then says honestly that the stop doesn't reach it.

/** Broker GET /health body, or null when nothing answers on its port. */
export type BrokerHealth = Record<string, unknown> | null;
/** Daemon GET /health body, or null when it isn't answering. */
export type DaemonHealth = Record<string, unknown> | null;

const MIN_BROKER_PROTOCOL = 2;

function obj(v: unknown): Record<string, unknown> | null {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
}

function sameStoppedGeneration(latch: OfficeLatch, seen: Record<string, unknown> | null): boolean {
  if (!seen || seen.state !== "stopped") return false;
  return (seen.generation ?? null) === latch.generation;
}

export function brokerStep(latch: OfficeLatch, broker: BrokerHealth): OfficeStopStep {
  if (!broker) return "not_running";
  if (typeof broker.protocol !== "number" || broker.protocol < MIN_BROKER_PROTOCOL) return "outdated";
  return sameStoppedGeneration(latch, obj(broker.latch)) ? "done" : "pending";
}

export function daemonStep(latch: OfficeLatch, daemon: DaemonHealth): { step: OfficeStopStep; activeTasks: number | null } {
  if (!daemon) return { step: "not_running", activeTasks: null };
  const s = obj(daemon.stop_latch);
  if (!s) return { step: "outdated", activeTasks: null };
  const activeTasks = typeof s.active_tasks === "number" ? s.active_tasks : null;
  const caughtUp = sameStoppedGeneration(latch, s);
  return { step: caughtUp && activeTasks === 0 ? "done" : "pending", activeTasks };
}

export function computeReceipt(latch: OfficeLatch, broker: BrokerHealth, daemon: DaemonHealth): OfficeStopReceipt | null {
  if (latch.state !== "stopped") return null;
  const d = daemonStep(latch, daemon);
  return {
    generation: latch.generation,
    requested: "done",
    inputSealed: brokerStep(latch, broker),
    tasksStopped: d.step,
    activeTasks: d.activeTasks,
  };
}
