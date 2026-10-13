"use client";

import { useMemo, useState } from "react";
import { Check } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  projectMembersOptions,
  useAddProjectMember,
  useRemoveProjectMember,
} from "@multica/core/projects";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { ActorAvatar } from "../../common/actor-avatar";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";

const MAX_STACKED_AVATARS = 3;

// "Shared with" property: the workspace members a project is shared with.
// The workspace owner edits the list; everyone else sees it read-only.
export function ProjectSharedWith({
  wsId,
  projectId,
  canEdit,
}: {
  wsId: string;
  projectId: string;
  canEdit: boolean;
}) {
  const { t } = useT("projects");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const { data: shared = [] } = useQuery(projectMembersOptions(wsId, projectId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const addMember = useAddProjectMember(wsId, projectId);
  const removeMember = useRemoveProjectMember(wsId, projectId);

  const sharedIds = useMemo(() => new Set(shared.map((m) => m.user_id)), [shared]);
  const nameOf = useMemo(() => {
    const names = new Map(members.map((m) => [m.user_id, m.name]));
    return (userId: string) => names.get(userId) ?? "";
  }, [members]);

  // Owners already see every project, so they are not offered for sharing.
  const candidates = useMemo(
    () => members.filter((m) => m.role !== "owner"),
    [members],
  );
  const query = filter.trim().toLowerCase();
  const visible = (canEdit ? candidates : candidates.filter((m) => sharedIds.has(m.user_id))).filter(
    (m) => !query || m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query),
  );

  const toggle = (userId: string) => {
    const mutation = sharedIds.has(userId) ? removeMember : addMember;
    mutation.mutate(userId, {
      onError: () => toast.error(t(($) => $.sharing.toast_failed)),
    });
  };

  const sharedList = shared.filter((m) => nameOf(m.user_id) !== "");

  return (
    <Popover open={open} onOpenChange={(v) => { setOpen(v); if (!v) setFilter(""); }}>
      <PopoverTrigger
        render={
          <button
            type="button"
            className="inline-flex min-w-0 items-center gap-1.5 text-caption hover:text-foreground transition-colors"
          >
            {sharedList.length === 0 ? (
              <span className="text-muted-foreground">{t(($) => $.sharing.not_shared)}</span>
            ) : sharedList.length === 1 ? (
              <>
                <ActorAvatar actorType="member" actorId={sharedList[0]!.user_id} size="sm" />
                <span className="truncate">{nameOf(sharedList[0]!.user_id)}</span>
              </>
            ) : (
              <>
                <span className="flex -space-x-1">
                  {sharedList.slice(0, MAX_STACKED_AVATARS).map((m) => (
                    <ActorAvatar key={m.user_id} actorType="member" actorId={m.user_id} size="sm" />
                  ))}
                </span>
                <span className="truncate">
                  {t(($) => $.sharing.count, { count: sharedList.length })}
                </span>
              </>
            )}
          </button>
        }
      />
      <PopoverContent align="start" className="w-56 max-w-[calc(100vw-2rem)] p-0">
        {canEdit && (
          <div className="px-2 py-1.5 border-b">
            <input
              type="text"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t(($) => $.sharing.search_placeholder)}
              className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
            />
          </div>
        )}
        <div className="p-1 max-h-60 overflow-y-auto">
          {visible.map((m) =>
            canEdit ? (
              <button
                type="button"
                key={m.user_id}
                aria-pressed={sharedIds.has(m.user_id)}
                onClick={() => toggle(m.user_id)}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body hover:bg-accent transition-colors"
              >
                <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                <span className="min-w-0 flex-1 truncate text-left">{m.name}</span>
                {sharedIds.has(m.user_id) && <Check className="h-3.5 w-3.5 shrink-0" />}
              </button>
            ) : (
              <div key={m.user_id} className="flex items-center gap-2 px-2 py-1.5 text-body">
                <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                <span className="min-w-0 flex-1 truncate">{m.name}</span>
              </div>
            ),
          )}
          {visible.length === 0 && (
            <div className="px-2 py-3 text-center text-body text-muted-foreground">
              {query
                ? t(($) => $.sharing.no_results)
                : canEdit
                  ? t(($) => $.sharing.no_members)
                  : t(($) => $.sharing.not_shared)}
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
