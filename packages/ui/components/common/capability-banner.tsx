import { Lock } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

type Resource = "agent" | "skill" | "comment" | "runtime" | "workspace";

type Reason =
  | "allowed"
  | "not_authenticated"
  | "not_member"
  | "not_owner_role"
  | "not_resource_owner"
  | "private_visibility"
  | "unknown";

const RESOURCE_NOUN: Record<Resource, string> = {
  agent: "agent",
  skill: "skill",
  comment: "comment",
  runtime: "runtime",
  workspace: "workspace",
};

/**
 * Read-only banner for resource detail pages — appears when the current user
 * cannot edit the resource. Single component owns all the copy variants so
 * the wording stays consistent across agent, skill, runtime detail pages.
 *
 * Returns `null` when the user *can* edit (reason === "allowed") so callers
 * can mount it unconditionally.
 */
export function CapabilityBanner({
  reason,
  resource,
  ownerName,
  className,
}: {
  reason: Reason;
  resource: Resource;
  /** Display name of the resource owner / creator. Optional — copy degrades gracefully. */
  ownerName?: string;
  className?: string;
}) {
  if (reason === "allowed" || reason === "unknown") return null;

  const noun = RESOURCE_NOUN[resource];
  const message = getCopy(reason, noun, ownerName);

  return (
    <div
      role="status"
      className={cn(
        "flex items-center gap-2 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-caption text-muted-foreground",
        className,
      )}
    >
      <Lock className="h-3.5 w-3.5 shrink-0" aria-hidden />
      <span>{message}</span>
    </div>
  );
}

function getCopy(reason: Reason, noun: string, ownerName?: string): string {
  switch (reason) {
    case "not_authenticated":
      return `Sign in to edit this ${noun}.`;
    case "not_member":
      return `Join this workspace to edit this ${noun}.`;
    case "not_owner_role":
      return `View only — only the workspace owner can manage this ${noun}.`;
    case "not_resource_owner":
      if (ownerName) {
        return `View only — only ${ownerName} and the workspace owner can edit this ${noun}.`;
      }
      return `View only — only the ${noun} owner and the workspace owner can edit this ${noun}.`;
    case "private_visibility":
      if (ownerName) {
        return `Personal ${noun} — only ${ownerName} can use this.`;
      }
      return `Personal ${noun} — only its owner can use this.`;
    case "allowed":
    case "unknown":
      return ""; // unreachable; component returned null above
  }
}
