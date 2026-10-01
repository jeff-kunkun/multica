import type { Progress } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

export function ProgressLine({ progress, className }: { progress?: Progress | null; className?: string }) {
  if (!progress?.text?.trim()) return null;
  const dot = progress.source === "close" ? "bg-emerald-500" : progress.source === "parking" ? "bg-amber-500" : "bg-blue-500";
  return <span className={cn("flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground", className)} title={progress.text}><span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full", dot)} /><span className="truncate">{progress.text}</span></span>;
}
