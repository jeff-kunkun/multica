"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { clientErrorMessage } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCloseIssue } from "@multica/core/issues/mutations";
import type { KnowledgeAudit } from "@multica/core/types";
import { projectMemoryLocationsOptions } from "@multica/core/projects/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";

const OUTCOMES = ["done", "in_review", "blocked", "cancelled"] as const;
type CloseOutcome = (typeof OUTCOMES)[number];

const fieldClass =
  "w-full rounded-md border border-border bg-background px-2 py-1.5 text-body";

export function CloseIssueDialog({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const issueId = typeof data?.issueId === "string" ? data.issueId : "";
  const identifier = typeof data?.identifier === "string" ? data.identifier : "";
  const wsId = useWorkspaceId();
  const closeIssue = useCloseIssue();
  const locations = useQuery(projectMemoryLocationsOptions(wsId));
  const [outcome, setOutcome] = useState<CloseOutcome>("done");
  const [evidence, setEvidence] = useState("");
  const [summary, setSummary] = useState("");
  const [noCode, setNoCode] = useState("");
  const [blockedBy, setBlockedBy] = useState("");
  const [mode, setMode] = useState<"none" | "changes" | null>(null);
  const [drafts, setDrafts] = useState<Record<string, { on: boolean; summary: string }>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const outcomeLabel = (key: CloseOutcome) => {
    switch (key) {
      case "done":
        return t(($) => $.close_issue.outcome_done);
      case "in_review":
        return t(($) => $.close_issue.outcome_in_review);
      case "blocked":
        return t(($) => $.close_issue.outcome_blocked);
      case "cancelled":
        return t(($) => $.close_issue.outcome_cancelled);
    }
  };

  const audit = (): KnowledgeAudit | undefined => {
    if (mode === "none") return { none: true };
    if (mode !== "changes") return undefined;
    const changes = (locations.data?.locations ?? [])
      .filter((location) => drafts[location.key]?.on)
      .map((location) => ({
        location: location.key,
        summary: drafts[location.key]?.summary ?? "",
      }));
    return { changes };
  };

  const submit = async () => {
    if (!issueId || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      await closeIssue.mutateAsync({
        id: issueId,
        outcome,
        evidence: evidence.trim(),
        summary: summary.trim() || undefined,
        no_code_reason:
          outcome === "done" || outcome === "in_review" ? noCode.trim() || undefined : undefined,
        blocked_by: outcome === "blocked" ? blockedBy.trim() || undefined : undefined,
        knowledge_audit: audit(),
      });
      onClose();
    } catch (err) {
      setError(clientErrorMessage(err) ?? t(($) => $.close_issue.failed));
      setSubmitting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !submitting) onClose(); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.close_issue.title, { identifier })}</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <label className="flex flex-col gap-1 text-body">
            {t(($) => $.close_issue.outcome)}
            <select
              className={fieldClass}
              value={outcome}
              onChange={(event) => setOutcome(event.target.value as CloseOutcome)}
            >
              {OUTCOMES.map((key) => (
                <option key={key} value={key}>
                  {outcomeLabel(key)}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-body">
            {t(($) => $.close_issue.evidence)}
            <Textarea
              value={evidence}
              onChange={(event) => setEvidence(event.target.value)}
              placeholder={t(($) => $.close_issue.evidence_placeholder)}
              required
            />
          </label>
          <label className="flex flex-col gap-1 text-body">
            {t(($) => $.close_issue.summary)}
            <Textarea
              value={summary}
              onChange={(event) => setSummary(event.target.value)}
              placeholder={t(($) => $.close_issue.summary_placeholder)}
            />
          </label>
          {(outcome === "done" || outcome === "in_review") && (
            <label className="flex flex-col gap-1 text-body">
              {t(($) => $.close_issue.no_code)}
              <Textarea value={noCode} onChange={(event) => setNoCode(event.target.value)} />
            </label>
          )}
          {outcome === "blocked" && (
            <label className="flex flex-col gap-1 text-body">
              {t(($) => $.close_issue.blocked_by)}
              <input
                className={fieldClass}
                value={blockedBy}
                placeholder={t(($) => $.close_issue.blocked_by_placeholder)}
                onChange={(event) => setBlockedBy(event.target.value)}
              />
            </label>
          )}
          <fieldset className="flex flex-col gap-2">
            <legend className="text-body">{t(($) => $.close_issue.knowledge)}</legend>
            <label className="flex items-center gap-2 text-body">
              <input
                type="radio"
                name="knowledge-mode"
                checked={mode === "none"}
                onChange={() => setMode("none")}
              />
              {t(($) => $.close_issue.knowledge_none)}
            </label>
            <label className="flex items-center gap-2 text-body">
              <input
                type="radio"
                name="knowledge-mode"
                checked={mode === "changes"}
                onChange={() => setMode("changes")}
              />
              {t(($) => $.close_issue.knowledge_changes)}
            </label>
            {mode === "changes" && locations.isPending && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.close_issue.locations_loading)}
              </p>
            )}
            {mode === "changes" && locations.isError && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.close_issue.locations_unavailable)}
              </p>
            )}
            {mode === "changes" &&
              (locations.data?.locations ?? []).map((location) => {
                const draft = drafts[location.key] ?? { on: false, summary: "" };
                return (
                  <div key={location.key} className="flex flex-col gap-1 pl-1">
                    <label className="flex items-center gap-2 text-body">
                      <input
                        type="checkbox"
                        checked={draft.on}
                        onChange={(event) =>
                          setDrafts((prev) => ({
                            ...prev,
                            [location.key]: { on: event.target.checked, summary: draft.summary },
                          }))
                        }
                      />
                      <span>{location.path}</span>
                      <span className="text-caption text-muted-foreground">{location.key}</span>
                    </label>
                    {draft.on && (
                      <Textarea
                        aria-label={location.key}
                        value={draft.summary}
                        placeholder={t(($) => $.close_issue.knowledge_summary_placeholder)}
                        onChange={(event) =>
                          setDrafts((prev) => ({
                            ...prev,
                            [location.key]: { on: true, summary: event.target.value },
                          }))
                        }
                      />
                    )}
                  </div>
                );
              })}
          </fieldset>
          {error && (
            <p role="alert" className="text-body text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={submitting} onClick={onClose}>
              {t(($) => $.close_issue.cancel)}
            </Button>
            <Button type="submit" disabled={submitting || evidence.trim() === ""}>
              {submitting ? t(($) => $.close_issue.submitting) : t(($) => $.close_issue.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
