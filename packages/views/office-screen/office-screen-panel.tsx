"use client";

import { useEffect, useState } from "react";
import { Check, ChevronDown, ChevronUp, Eye, EyeOff, Loader2, OctagonX, TriangleAlert } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import {
  useOfficeScreen,
  type OfficePagePreview,
  type OfficeScreenControls,
  type OfficeStopStep,
} from "../platform/office-screen";

// The office screen (DENE-2261): the red stop for every agent on this Mac,
// the receipt that each process confirmed it, and a read-only live picture
// of the virtual display they browse on. Desktop only; renders nothing on web.

const STALE_MS = 5_000;
const EXPANDED_KEY = "multica:office-screen:expanded";

type T = ReturnType<typeof useT<"office">>["t"];

function StepRow({ label, step, activeTasks, t }: { label: string; step: OfficeStopStep; activeTasks?: number | null; t: T }) {
  const text =
    step === "done"
      ? t(($) => $.screen.step_done)
      : step === "not_running"
        ? t(($) => $.screen.step_not_running)
        : step === "outdated"
          ? t(($) => $.screen.step_outdated)
          : activeTasks
            ? t(($) => $.screen.step_pending_tasks, { n: activeTasks })
            : t(($) => $.screen.step_pending);
  return (
    <li className="flex items-start gap-2 text-label" data-step={step}>
      {step === "done" || step === "not_running" ? (
        <Check className="mt-0.5 size-3.5 shrink-0 text-emerald-600" />
      ) : step === "outdated" ? (
        <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-amber-600" />
      ) : (
        <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin text-muted-foreground" />
      )}
      <span className="font-medium">{label}</span>
      <span className={cn("text-muted-foreground", step === "outdated" && "text-amber-700 dark:text-amber-500")}>{text}</span>
    </li>
  );
}

function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), 1_000);
    return () => clearInterval(id);
  }, [active]);
  return now;
}

function blockedText(reason: string | null, t: T): string {
  switch (reason) {
    case "unowned_window":
      return t(($) => $.screen.view_unowned_window);
    case "window_straddles_display":
      return t(($) => $.screen.view_straddles);
    case "vd_missing":
      return t(($) => $.screen.view_vd_missing);
    case "vd_is_main":
    case "not_office_display":
      return t(($) => $.screen.view_not_office_display);
    default:
      return t(($) => $.screen.view_interrupted);
  }
}

function Picture({ c, t }: { c: OfficeScreenControls; t: T }) {
  const view = c.state!.view;
  const [previews, setPreviews] = useState<OfficePagePreview[] | null>(null);
  const now = useNow(view.status === "streaming");
  const permissionBlocked = view.status === "blocked" && view.reason === "permission";

  // Degraded previews refresh while they're open and the live picture can't run.
  const previewsOpen = previews !== null;
  const { pagePreviews } = c;
  useEffect(() => {
    if (!previewsOpen || !permissionBlocked) return;
    const id = setInterval(() => void pagePreviews().then(setPreviews), 5_000);
    return () => clearInterval(id);
  }, [previewsOpen, permissionBlocked, pagePreviews]);

  if (!view.enabled) {
    return <p className="text-label text-muted-foreground">{t(($) => $.screen.view_hidden)}</p>;
  }
  const box = "flex aspect-video w-full items-center justify-center rounded-md border bg-muted/40 p-4 text-center text-label text-muted-foreground";

  if (view.status === "streaming" && view.reason !== "no_windows" && c.frame) {
    const stale = now - c.frame.at > STALE_MS;
    return (
      <figure className="space-y-1">
        <img
          src={c.frame.src}
          alt={t(($) => $.screen.title)}
          className={cn("aspect-video w-full rounded-md border bg-black object-contain", stale && "opacity-50")}
        />
        <figcaption className="text-caption text-muted-foreground">
          {stale ? t(($) => $.screen.view_stale) : t(($) => $.screen.view_live)}
        </figcaption>
      </figure>
    );
  }
  if (view.status === "streaming" && view.reason === "no_windows") {
    return <div className={box}>{t(($) => $.screen.view_no_windows)}</div>;
  }
  if (view.status === "starting" || (view.status === "streaming" && !c.frame) || view.status === "off") {
    return (
      <div className={box}>
        <Loader2 className="mr-2 size-4 animate-spin" />
        {t(($) => $.screen.view_starting)}
      </div>
    );
  }
  if (view.status === "unavailable") {
    return <div className={box}>{t(($) => $.screen.view_unavailable)}</div>;
  }
  if (permissionBlocked) {
    return (
      <div className="space-y-2">
        <div className={cn(box, "flex-col gap-2")}>
          <span>{t(($) => $.screen.view_permission, { app: t(($) => $.screen.app_name) })}</span>
          <span className="flex gap-2">
            <Button size="xs" variant="outline" onClick={() => void c.requestPermission()}>
              {t(($) => $.screen.view_permission_allow)}
            </Button>
            <Button size="xs" variant="ghost" onClick={c.openPermissionSettings}>
              {t(($) => $.screen.view_permission_settings)}
            </Button>
            {previews === null && (
              <Button size="xs" variant="ghost" onClick={() => void c.pagePreviews().then(setPreviews)}>
                {t(($) => $.screen.view_previews)}
              </Button>
            )}
          </span>
        </div>
        {previews !== null && (
          <section className="space-y-1">
            <p className="text-caption text-muted-foreground">{t(($) => $.screen.view_previews_title)}</p>
            {previews.length === 0 ? (
              <p className="text-label text-muted-foreground">{t(($) => $.screen.view_previews_empty)}</p>
            ) : (
              <div className="grid grid-cols-2 gap-2">
                {previews.map((p) => (
                  <img key={p.targetId} src={p.src} alt="" className="w-full rounded border object-contain" />
                ))}
              </div>
            )}
          </section>
        )}
      </div>
    );
  }
  return <div className={box}>{blockedText(view.reason, t)}</div>;
}

function StopControls({ c, t }: { c: OfficeScreenControls; t: T }) {
  const s = c.state!;
  const [busy, setBusy] = useState(false);
  const [changed, setChanged] = useState(false);
  const stopped = s.latch.state === "stopped";

  if (!stopped) {
    return (
      <Button
        size="sm"
        className="bg-destructive text-white hover:bg-destructive/90"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          try {
            await c.stop();
          } finally {
            setBusy(false);
          }
        }}
      >
        <OctagonX />
        {busy ? t(($) => $.screen.stopping) : t(($) => $.screen.stop)}
      </Button>
    );
  }
  return (
    <div className="flex flex-col items-end gap-1">
      <Button
        size="sm"
        variant="outline"
        disabled={busy || s.latch.generation === null}
        title={t(($) => $.screen.resume_hint)}
        onClick={async () => {
          setBusy(true);
          try {
            setChanged(!(await c.resume()));
          } finally {
            setBusy(false);
          }
        }}
      >
        {t(($) => $.screen.resume)}
      </Button>
      {changed && <span className="text-caption text-amber-700 dark:text-amber-500">{t(($) => $.screen.resume_changed)}</span>}
    </div>
  );
}

function StatusPill({ stopped, t }: { stopped: boolean; t: T }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-caption font-medium",
        stopped ? "bg-destructive/10 text-destructive" : "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
      )}
      data-testid="office-screen-status"
    >
      <span className={cn("size-1.5 rounded-full", stopped ? "bg-destructive" : "bg-emerald-500")} />
      {stopped ? t(($) => $.screen.stopped) : t(($) => $.screen.running)}
    </span>
  );
}

function Receipt({ c, t }: { c: OfficeScreenControls; t: T }) {
  const s = c.state!;
  return (
    <>
      {s.latch.readError && (
        <p className="text-label text-amber-700 dark:text-amber-500">{t(($) => $.screen.unreadable)}</p>
      )}
      {s.receipt && (
        <ul className="space-y-1" aria-label={t(($) => $.screen.stopped)}>
          <StepRow label={t(($) => $.screen.step_requested)} step={s.receipt.requested} t={t} />
          <StepRow label={t(($) => $.screen.step_input)} step={s.receipt.inputSealed} t={t} />
          <StepRow label={t(($) => $.screen.step_tasks)} step={s.receipt.tasksStopped} activeTasks={s.receipt.activeTasks} t={t} />
        </ul>
      )}
    </>
  );
}

function Details({ c, t }: { c: OfficeScreenControls; t: T }) {
  const s = c.state!;
  return (
    <div className="space-y-3">
      <Receipt c={c} t={t} />
      <Picture c={c} t={t} />
      <div className="flex items-center justify-between gap-2">
        <p className="text-caption text-muted-foreground">{t(($) => $.screen.read_only_note)}</p>
        <Button size="xs" variant="ghost" onClick={() => void c.setViewEnabled(!s.view.enabled)}>
          {s.view.enabled ? <EyeOff /> : <Eye />}
          {s.view.enabled ? t(($) => $.screen.view_off) : t(($) => $.screen.view_on)}
        </Button>
      </div>
    </div>
  );
}

/**
 * variant "bar": one row on the workspace landing page (status, stop, live
 * thumbnail) that expands into the full panel. variant "card": the full
 * panel, for an agent's detail page.
 */
export function OfficeScreenPanel({ variant = "card", className }: { variant?: "bar" | "card"; className?: string }) {
  const { t } = useT("office");
  const c = useOfficeScreen();
  const [expanded, setExpanded] = useState(() => {
    try {
      return window.localStorage.getItem(EXPANDED_KEY) === "1";
    } catch {
      return false;
    }
  });

  if (!c.available || !c.state?.supported) return null;
  const s = c.state;
  const stopped = s.latch.state === "stopped";

  if (variant === "bar") {
    const toggle = () => {
      const next = !expanded;
      setExpanded(next);
      try {
        window.localStorage.setItem(EXPANDED_KEY, next ? "1" : "0");
      } catch {
        // Not persisted; fine.
      }
    };
    const showThumb = !expanded && s.view.enabled && s.view.status === "streaming" && c.frame;
    return (
      <section
        className={cn("border-b px-4 py-2", stopped && "bg-destructive/5", className)}
        data-testid="office-screen-bar"
      >
        <div className="flex items-center gap-3">
          <span className="text-label font-medium">{t(($) => $.screen.title)}</span>
          <StatusPill stopped={stopped} t={t} />
          <span className="flex-1" />
          {showThumb && (
            <button type="button" onClick={toggle} className="h-9 overflow-hidden rounded border" aria-label={t(($) => $.screen.expand)}>
              <img src={c.frame!.src} alt="" className="h-full w-16 bg-black object-contain" />
            </button>
          )}
          <StopControls c={c} t={t} />
          <Button size="icon-sm" variant="ghost" onClick={toggle} aria-label={expanded ? t(($) => $.screen.collapse) : t(($) => $.screen.expand)}>
            {expanded ? <ChevronUp /> : <ChevronDown />}
          </Button>
        </div>
        {(expanded || (stopped && s.receipt)) && (
          <div className="mx-auto mt-3 max-w-3xl pb-2">
            {expanded ? <Details c={c} t={t} /> : <div className="space-y-2"><Receipt c={c} t={t} /></div>}
          </div>
        )}
      </section>
    );
  }

  return (
    <section className={cn("space-y-3 rounded-lg border bg-card p-4", className)} data-testid="office-screen-card">
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <span className="text-body font-medium">{t(($) => $.screen.title)}</span>
            <StatusPill stopped={stopped} t={t} />
          </div>
          <p className="text-caption text-muted-foreground">{t(($) => $.screen.scope_this_mac)}</p>
        </div>
        <StopControls c={c} t={t} />
      </div>
      <Details c={c} t={t} />
    </section>
  );
}
