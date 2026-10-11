"use client";

import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { fastAccessOptions } from "@multica/core/inbox/fast-access";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n/use-t";

export function FastAccessDialog({ wsId }: { wsId: string }) {
  const { t } = useT("inbox");
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button variant="ghost" className="min-h-11 shrink-0 px-2" />}>
        {t(($) => $.fast_access.open)}
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl" showCloseButton={false}>
        <DialogHeader className="flex-row items-center justify-between gap-2 text-left">
          <DialogTitle>{t(($) => $.fast_access.title)}</DialogTitle>
          <Button variant="ghost" className="min-h-11" onClick={() => setOpen(false)}>{t(($) => $.fast_access.close)}</Button>
        </DialogHeader>
        {open && <FastAccessContent wsId={wsId} />}
      </DialogContent>
    </Dialog>
  );
}

function FastAccessContent({ wsId }: { wsId: string }) {
  const { t } = useT("inbox");
  const userId = useAuthStore((state) => state.user?.id ?? "");
  const [allVisible, setAllVisible] = useState(true);
  const [since, setSince] = useState(() => new Date(Date.now() - 30 * 60_000).toISOString());
  const query = useInfiniteQuery({ ...fastAccessOptions(userId, wsId, allVisible, since), enabled: userId.length > 0 });
  const pages = query.data?.pages ?? [];
  const last = pages.at(-1);
  // Never retain a partially loaded batch after a continuation error: access may have changed.
  const runs = query.isError ? [] : pages.flatMap((page) => page.activeRuns);
  const chats = query.isError ? [] : pages.flatMap((page) => page.changes).filter((item) => item.kind === "chat");
  const phaseLabel = (phase?: string) => {
    switch (phase) {
      case "executing": return t(($) => $.fast_access.executing);
      case "queued": return t(($) => $.fast_access.queued);
      case "waiting": return t(($) => $.fast_access.waiting);
      default: return t(($) => $.fast_access.unknown);
    }
  };
  const refresh = () => setSince(new Date(Date.now() - 30 * 60_000).toISOString());
  return (
    <div className="min-w-0 space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex min-w-0 flex-1 flex-col gap-1 text-caption">
          {t(($) => $.fast_access.scope)}
          <select className="min-h-11 max-w-full rounded-md border border-input bg-background px-2 text-body" value={allVisible ? "all" : "current"} onChange={(event) => setAllVisible(event.target.value === "all")}>
            <option value="all">{t(($) => $.fast_access.all)}</option>
            <option value="current">{t(($) => $.fast_access.current)}</option>
          </select>
        </label>
        <Button variant="outline" className="min-h-11 self-end" onClick={refresh} disabled={query.isFetching}>{t(($) => $.fast_access.refresh)}</Button>
      </div>
      {query.isPending && <p role="status">{t(($) => $.fast_access.loading)}</p>}
      {query.isError && <p role="alert">{t(($) => $.fast_access.error)}</p>}
      {!query.isPending && !query.isError && <>
        {last && <p className="text-caption text-muted-foreground">{t(($) => $.fast_access.as_of, { time: new Date(last.asOf).toLocaleString() })}</p>}
        <section className="space-y-2">
          <h3 className="text-body font-medium">{t(($) => $.fast_access.runs)} · {runs.length}{!last?.activeRunsComplete && "+"}</h3>
          {runs.map((run) => <div key={`${run.workspaceId}:${run.id}`} className="border-b border-border py-2 text-body">
            {allVisible && run.workspaceName && <p className="break-words text-caption text-muted-foreground">{run.workspaceName}</p>}
            <p className="break-words">{run.title || t(($) => $.fast_access.untitled_run)}</p>
            <p className="text-caption text-muted-foreground">{phaseLabel(run.phase)}{run.sourceType === "autopilot" && ` · ${t(($) => $.fast_access.autopilot)}`}</p>
          </div>)}
          {!runs.length && last?.activeRunsComplete && <p className="text-muted-foreground">{t(($) => $.fast_access.no_runs)}</p>}
        </section>
        <section className="space-y-2">
          <h3 className="text-body font-medium">{t(($) => $.fast_access.chats)}</h3>
          {chats.map((chat) => <div key={`${chat.workspaceId}:${chat.id}`} className="border-b border-border py-2 text-body">
            {allVisible && chat.workspaceName && <p className="break-words text-caption text-muted-foreground">{chat.workspaceName}</p>}
            <div className="flex flex-wrap justify-between gap-1"><p className="min-w-0 break-words">{chat.title || t(($) => $.fast_access.untitled_chat)}</p><span className="text-caption text-muted-foreground">{t(($) => $.fast_access.unread, { count: chat.unreadCount ?? 0 })}</span></div>
            {chat.preview && <p className="whitespace-pre-wrap break-words text-muted-foreground">{chat.preview}{chat.contentTruncated && "..."}</p>}
            <time className="text-caption text-muted-foreground" dateTime={chat.lastActiveAt ?? chat.changedAt}>{new Date(chat.lastActiveAt ?? chat.changedAt).toLocaleString()}</time>
          </div>)}
          {!chats.length && last?.complete && <p className="text-muted-foreground">{t(($) => $.fast_access.no_chats)}</p>}
        </section>
        <p className="text-caption text-muted-foreground" role="status">{last?.complete ? t(($) => $.fast_access.complete) : t(($) => $.fast_access.partial)}</p>
        {query.hasNextPage && <Button className="min-h-11" variant="outline" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>{query.isFetchingNextPage ? t(($) => $.fast_access.loading) : t(($) => $.fast_access.more)}</Button>}
      </>}
    </div>
  );
}
