"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ClipboardList } from "lucide-react";
import { useFeatureEnabled } from "@multica/core/config";
import { OFFICE_PLAN_FIRST_FLAG } from "@multica/core/feature-flags";
import {
  issuePlansOptions,
  useApproveIssuePlan,
  useRequestIssuePlanChanges,
} from "@multica/core/plans";
import type { IssuePlan } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

type PlanState = "waiting" | "approved" | "changes" | "superseded";

function planState(plan: IssuePlan): PlanState {
  if (plan.status === "approved") return "approved";
  if (plan.status === "superseded") return "superseded";
  return plan.changes_requested_at ? "changes" : "waiting";
}

/**
 * Plan first (DENE-2670). On an issue created with "plan first" it shows the
 * newest plan the agent proposed, with "Approve" and "Change it first" for the
 * people who may answer. Both answers are replies under the plan card, so the
 * agent continues in the same session. Hidden unless this client was built
 * with the office flag, and on issues that do not ask for a plan.
 */
export function IssuePlanCard({ issueId }: { issueId: string }) {
  const enabled = useFeatureEnabled(OFFICE_PLAN_FIRST_FLAG);
  const { data } = useQuery({ ...issuePlansOptions(issueId), enabled: enabled && !!issueId });
  if (!enabled || !data?.gated) return null;
  const latest = data.plans[0];
  return (
    <section
      aria-labelledby={`issue-plan-${issueId}`}
      className="mt-6 rounded-lg border border-border bg-card p-4"
    >
      {latest ? (
        <PlanBody
          key={latest.id}
          issueId={issueId}
          plan={latest}
          canApprove={data.can_approve}
        />
      ) : (
        <EmptyPlan issueId={issueId} />
      )}
    </section>
  );
}

function EmptyPlan({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  return (
    <div className="flex items-start gap-2">
      <ClipboardList className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0">
        <h2 id={`issue-plan-${issueId}`} className="text-label font-medium">
          {t(($) => $.plan_first.title)}
        </h2>
        <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.plan_first.waiting_for_plan)}</p>
      </div>
    </div>
  );
}

function PlanBody({ issueId, plan, canApprove }: { issueId: string; plan: IssuePlan; canApprove: boolean }) {
  const { t } = useT("issues");
  const approve = useApproveIssuePlan(issueId);
  const requestChanges = useRequestIssuePlanChanges(issueId);
  const [editing, setEditing] = useState(false);
  const [feedback, setFeedback] = useState("");
  // The approval reached no agent; approving again retries the wake.
  const [notWoken, setNotWoken] = useState(false);
  const state = planState(plan);
  const canAnswer = canApprove && plan.status === "proposed";
  const busy = approve.isPending || requestChanges.isPending;

  const stateLabel = {
    waiting: t(($) => $.plan_first.state_waiting),
    approved: t(($) => $.plan_first.state_approved),
    changes: t(($) => $.plan_first.state_changes),
    superseded: t(($) => $.plan_first.state_superseded),
  }[state];

  const onApprove = () =>
    approve.mutate(plan.version, {
      onSuccess: (answer) => {
        const missed = answer.wake?.none_woken === true;
        setNotWoken(missed);
        if (missed) toast.error(t(($) => $.plan_first.not_woken_toast));
        else toast.success(t(($) => $.plan_first.approved_toast, { version: plan.version }));
      },
      onError: () => toast.error(t(($) => $.plan_first.answer_failed)),
    });

  const onSendBack = () => {
    const note = feedback.trim();
    if (!note) return;
    requestChanges.mutate(
      { version: plan.version, feedback: note },
      {
        onSuccess: () => {
          setEditing(false);
          setFeedback("");
          toast.success(t(($) => $.plan_first.changes_toast, { version: plan.version }));
        },
        onError: () => toast.error(t(($) => $.plan_first.answer_failed)),
      },
    );
  };

  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <ClipboardList className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <h2 id={`issue-plan-${issueId}`} className="text-label font-medium">
          {t(($) => $.plan_first.version_title, { version: plan.version })}
        </h2>
        <span
          role="status"
          className={
            state === "approved"
              ? "rounded-full bg-success/10 px-2 py-0.5 text-caption text-success"
              : "rounded-full border border-border px-2 py-0.5 text-caption text-muted-foreground"
          }
        >
          {stateLabel}
        </span>
      </div>

      <dl className="mt-3 grid gap-3 text-body">
        <PlanField label={t(($) => $.plan_first.goal)}>{plan.goal}</PlanField>
        <PlanField label={t(($) => $.plan_first.out_of_scope)}>{plan.out_of_scope}</PlanField>
        <PlanField label={t(($) => $.plan_first.steps)}>
          <ol className="list-decimal space-y-0.5 pl-5">
            {plan.steps.map((step, i) => (
              <li key={i} className="break-words">
                {step.title}
              </li>
            ))}
          </ol>
        </PlanField>
        <PlanField label={t(($) => $.plan_first.done_when)}>{plan.done_when}</PlanField>
      </dl>

      {canAnswer && !editing && (
        <div className="mt-4 flex flex-wrap gap-2">
          <Button size="sm" onClick={onApprove} disabled={busy}>
            {t(($) => $.plan_first.approve)}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setEditing(true)} disabled={busy}>
            {t(($) => $.plan_first.request_changes)}
          </Button>
        </div>
      )}
      {canApprove && notWoken && state === "approved" && (
        <div className="mt-4">
          <Button size="sm" variant="outline" onClick={onApprove} disabled={busy}>
            {t(($) => $.plan_first.retry_wake)}
          </Button>
        </div>
      )}
      {canAnswer && editing && (
        <div className="mt-4 grid gap-2">
          <label htmlFor={`issue-plan-feedback-${issueId}`} className="text-caption text-muted-foreground">
            {t(($) => $.plan_first.feedback_label)}
          </label>
          <Textarea
            id={`issue-plan-feedback-${issueId}`}
            value={feedback}
            onChange={(e) => setFeedback(e.target.value)}
            placeholder={t(($) => $.plan_first.feedback_placeholder)}
            rows={3}
            autoFocus
          />
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={onSendBack} disabled={busy || !feedback.trim()}>
              {t(($) => $.plan_first.send_back)}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)} disabled={busy}>
              {t(($) => $.plan_first.cancel)}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

function PlanField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-caption text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 whitespace-pre-wrap break-words">{children}</dd>
    </div>
  );
}
