"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Users } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { meetingListOptions, planKeys } from "@multica/core/plans";
import { useCreateIssue } from "@multica/core/issues/mutations";
import type { Meeting } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../layout/collection-page";
import { AppLink, useNavigation } from "../navigation";
import { ActorAvatar } from "../common/actor-avatar";
import { IssueAgentActivityIndicator } from "../issues/components/issue-agent-activity-indicator";
import { useT } from "../i18n";
import {
  ActorOptionList,
  actorKey,
  mentionMarkdown,
  useActorOptions,
  type ActorRef,
} from "./actor-options";

const MAX_ATTENDEES = 3;

export function MeetingsPage() {
  const { t } = useT("office");
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { data: meetings = [], isLoading } = useQuery(meetingListOptions(wsId));
  const [open, setOpen] = useState(false);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CollectionPageHeader
        icon={Users}
        title={t(($) => $.meetings.title)}
        count={meetings.length}
        actions={
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.meetings.new)}
            onClick={() => setOpen(true)}
          />
        }
      />
      {!isLoading && meetings.length === 0 ? (
        <CollectionPageState
          icon={Users}
          title={t(($) => $.meetings.empty)}
          description={t(($) => $.meetings.empty_hint)}
          actions={
            <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
              {t(($) => $.meetings.new)}
            </Button>
          }
        />
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {meetings.map((meeting) => (
            <li key={meeting.issue.id} className="border-b">
              <AppLink
                href={p.issueDetail(meeting.issue.id)}
                className="flex min-h-14 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-accent/50 md:px-6"
              >
                {meeting.issue.assignee_type && meeting.issue.assignee_id ? (
                  <ActorAvatar
                    actorType={meeting.issue.assignee_type}
                    actorId={meeting.issue.assignee_id}
                    size="sm"
                    profileLink={false}
                  />
                ) : (
                  <span className="size-5 shrink-0" aria-hidden="true" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-body">{meeting.issue.title}</div>
                  <div className="flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
                    <span className="shrink-0">{meeting.issue.identifier}</span>
                    <MeetingStatus meeting={meeting} />
                  </div>
                </div>
                <IssueAgentActivityIndicator issueId={meeting.issue.id} hoverCard={false} />
              </AppLink>
            </li>
          ))}
        </ul>
      )}
      <StartMeetingDialog open={open} onOpenChange={setOpen} />
    </div>
  );
}

function MeetingStatus({ meeting }: { meeting: Meeting }) {
  const { t } = useT("office");
  const status = meeting.plan?.status;
  if (meeting.steps_total > 0 && status !== "proposed") {
    return (
      <span className="truncate">
        {t(($) => $.meetings.status_approved, {
          done: meeting.steps_done,
          total: meeting.steps_total,
        })}
      </span>
    );
  }
  if (status === "proposed") {
    return <span className="truncate text-info">{t(($) => $.meetings.status_awaiting)}</span>;
  }
  return <span className="truncate">{t(($) => $.meetings.status_discussing)}</span>;
}

/**
 * "开个会": one question, up to three people. One person is picked by default
 * (meetings are usually one agent drafting for you); the first pick writes
 * the plan and becomes the assignee, so their run starts on creation.
 */
function StartMeetingDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("office");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const p = useWorkspacePaths();
  const navigation = useNavigation();
  const options = useActorOptions();
  const createIssue = useCreateIssue();
  const [question, setQuestion] = useState("");
  const [picked, setPicked] = useState<ActorRef[] | null>(null);
  // Default: the first available agent, until the user changes the pick.
  const attendees = picked ?? options.slice(0, 1);
  const lead = attendees[0];

  const toggle = (actor: ActorRef) => {
    const key = actorKey(actor);
    if (attendees.some((a) => actorKey(a) === key)) {
      setPicked(attendees.filter((a) => actorKey(a) !== key));
    } else if (attendees.length < MAX_ATTENDEES) {
      setPicked([...attendees, actor]);
    }
  };

  const reset = () => {
    setQuestion("");
    setPicked(null);
  };

  const start = () => {
    const text = question.trim();
    if (!text || !lead) return;
    const others = attendees.slice(1);
    const brief = [
      text,
      "",
      t(($) => $.meetings.brief_intro),
      others.length > 0
        ? t(($) => $.meetings.brief_others, { names: others.map(mentionMarkdown).join(" ") })
        : null,
    ]
      .filter((line) => line !== null)
      .join("\n");
    createIssue.mutate(
      {
        title: text.split("\n")[0]!.slice(0, 120),
        description: brief,
        assignee_type: lead.type,
        assignee_id: lead.id,
        meeting: true,
      },
      {
        onSuccess: (issue) => {
          qc.invalidateQueries({ queryKey: planKeys.meetings(wsId) });
          onOpenChange(false);
          reset();
          navigation.push(p.issueDetail(issue.id));
        },
        onError: () => toast.error(t(($) => $.meetings.create_failed)),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.meetings.dialog_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.meetings.members_hint)}</DialogDescription>
        </DialogHeader>
        <label className="flex flex-col gap-1.5 text-caption text-muted-foreground">
          {t(($) => $.meetings.question_label)}
          <Textarea
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder={t(($) => $.meetings.question_placeholder)}
            rows={3}
            className="text-body text-foreground"
          />
        </label>
        <div className="flex flex-col gap-1.5">
          <span className="text-caption text-muted-foreground">
            {t(($) => $.meetings.members_label)}
          </span>
          <ActorOptionList
            options={options}
            selected={attendees}
            onToggle={toggle}
            badge={(_, index) => (index === 0 ? t(($) => $.meetings.lead) : null)}
          />
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>
            {t(($) => $.meetings.cancel)}
          </DialogClose>
          <Button onClick={start} disabled={!question.trim() || !lead || createIssue.isPending}>
            {createIssue.isPending ? t(($) => $.meetings.starting) : t(($) => $.meetings.start)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
