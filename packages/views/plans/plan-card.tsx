"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { issuePlansOptions, useApprovePlan } from "@multica/core/plans";
import { childIssuesOptions } from "@multica/core/issues/queries";
import { issueStatusCategory } from "@multica/core/issues";
import { useCreateComment } from "@multica/core/issues/mutations";
import { useActorName } from "@multica/core/workspace/hooks";
import type { Plan } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { ActorAvatar } from "../common/actor-avatar";
import { IssueMentionCard } from "../issues/components/issue-mention-card";
import { useT } from "../i18n";
import {
  ActorOptionList,
  mentionMarkdown,
  useActorOptions,
  type ActorRef,
} from "./actor-options";

/**
 * The newest plan on an issue, shown above the timeline: what it is for, what
 * it leaves out, who does each step, and the two decisions a reader makes —
 * approve it, or ask someone to cut it down. Renders nothing without a plan.
 */
export function IssuePlanCard({ issueId }: { issueId: string }) {
  const wsId = useWorkspaceId();
  const { data } = useQuery(issuePlansOptions(wsId, issueId));
  const plan = data?.plans[0];
  if (!plan) return null;
  return <PlanCard plan={plan} canApprove={data.can_approve} />;
}

function PlanCard({ plan, canApprove }: { plan: Plan; canApprove: boolean }) {
  const { t } = useT("office");
  const wsId = useWorkspaceId();
  const { getActorName } = useActorName();
  const approve = useApprovePlan(wsId);
  const approved = plan.status === "approved";
  const { data: children = [] } = useQuery({
    ...childIssuesOptions(wsId, plan.issue_id),
    enabled: approved,
  });

  const stepIssueIds = plan.steps.flatMap((s) => (s.issue_id ? [s.issue_id] : []));
  const done = children.filter((c) => {
    if (!stepIssueIds.includes(c.id)) return false;
    const category = issueStatusCategory(c);
    return category === "done" || category === "closed";
  }).length;

  const statusLabel =
    plan.status === "approved"
      ? t(($) => $.plan.status_approved)
      : plan.status === "superseded"
        ? t(($) => $.plan.status_superseded)
        : t(($) => $.plan.status_proposed);

  const onApprove = () => {
    approve.mutate(
      { issueId: plan.issue_id, version: plan.version },
      { onError: () => toast.error(t(($) => $.plan.approve_failed)) },
    );
  };

  return (
    <section
      aria-label={t(($) => $.plan.title, { version: plan.version })}
      className="mb-6 rounded-lg border bg-card p-4"
    >
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-body font-medium">
          {t(($) => $.plan.title, { version: plan.version })}
        </h2>
        <span className="text-caption text-muted-foreground">
          {approved && stepIssueIds.length > 0
            ? `${statusLabel} · ${t(($) => $.plan.progress, { done, total: stepIssueIds.length })}`
            : statusLabel}
        </span>
      </header>

      <dl className="mt-3 flex flex-col gap-3 text-body">
        <PlanField label={t(($) => $.plan.goal)} value={plan.goal} />
        {plan.out_of_scope && (
          <PlanField label={t(($) => $.plan.out_of_scope)} value={plan.out_of_scope} />
        )}
        <div>
          <dt className="text-caption text-muted-foreground">{t(($) => $.plan.steps)}</dt>
          <dd>
            <ol className="mt-1 flex flex-col gap-1.5">
              {plan.steps.map((step, i) => (
                <li key={i} className="flex min-w-0 items-center gap-2">
                  <ActorAvatar
                    actorType={step.assignee_type}
                    actorId={step.assignee_id}
                    size="xs"
                  />
                  {/* Once approved the step is a task; its chip already carries the title and status. */}
                  {step.issue_id ? (
                    <span className="min-w-0 flex-1 truncate">
                      <IssueMentionCard issueId={step.issue_id} fallbackLabel={step.title} />
                    </span>
                  ) : (
                    <span className="min-w-0 flex-1 truncate">{step.title}</span>
                  )}
                </li>
              ))}
            </ol>
          </dd>
        </div>
        {plan.done_when && (
          <PlanField label={t(($) => $.plan.done_when)} value={plan.done_when} />
        )}
      </dl>

      {plan.status === "proposed" && (
        <footer className="mt-4 flex flex-col gap-2">
          <div className="flex flex-wrap gap-2">
            <Button
              onClick={onApprove}
              disabled={!canApprove || approve.isPending}
              className="min-w-24"
            >
              {approve.isPending ? t(($) => $.plan.approving) : t(($) => $.plan.approve)}
            </Button>
            <CutDialog plan={plan} />
          </div>
          {!canApprove && (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.plan.approve_disabled)}
            </p>
          )}
        </footer>
      )}
      {approved && plan.approved_by && (
        <p className="mt-3 text-caption text-muted-foreground">
          {t(($) => $.plan.approved_by, { name: getActorName("member", plan.approved_by) })}
        </p>
      )}
    </section>
  );
}

function PlanField({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-caption text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 whitespace-pre-wrap break-words">{value}</dd>
    </div>
  );
}

/** "请人砍一刀": a reply under the plan that @-mentions one person. */
function CutDialog({ plan }: { plan: Plan }) {
  const { t } = useT("office");
  const options = useActorOptions();
  const [open, setOpen] = useState(false);
  const [picked, setPicked] = useState<ActorRef | null>(null);
  const createComment = useCreateComment(plan.issue_id);

  const send = () => {
    if (!picked) return;
    createComment.mutate(
      {
        content: `${mentionMarkdown(picked)} ${t(($) => $.plan.cut_message)}`,
        parentId: plan.comment_id ?? undefined,
      },
      {
        onSuccess: () => {
          toast.success(t(($) => $.plan.cut_sent));
          setOpen(false);
          setPicked(null);
        },
        onError: () => toast.error(t(($) => $.plan.cut_failed)),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button variant="outline" />}>
        {t(($) => $.plan.cut)}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.plan.cut_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.plan.cut_hint)}</DialogDescription>
        </DialogHeader>
        <ActorOptionList
          options={options}
          selected={picked ? [picked] : []}
          onToggle={(actor) => setPicked(actor)}
        />
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>
            {t(($) => $.meetings.cancel)}
          </DialogClose>
          <Button onClick={send} disabled={!picked || createComment.isPending}>
            {t(($) => $.plan.cut_send)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
