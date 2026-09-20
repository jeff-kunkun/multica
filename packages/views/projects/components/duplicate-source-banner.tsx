"use client";

import { useState } from "react";
import { AlertTriangle } from "lucide-react";
import type { DuplicateSourceGroup } from "@multica/core/projects/source-rule";
import type { ProjectResource } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

/**
 * Inline warning for a repository configured twice — once as a remote URL, once
 * as a directory on a machine.
 *
 * Silence was the bug (DENE-595): the project saved both, said nothing, and
 * tasks then cloned a second copy of code the machine already held. So this
 * banner sits in the resource list rather than firing a toast at save time —
 * the duplicate is a standing property of the configuration, not a moment.
 *
 * The merge action removes the redundant `github_repo` rows and keeps the local
 * directory, because that is the rule the runtime follows. It never touches the
 * local row: deleting the thing the user explicitly pointed at is not a merge.
 * And because this match is by repository NAME — all a browser can compare —
 * the action is always the user's to take, never automatic.
 */
export function DuplicateSourceBanner({
  groups,
  onMerge,
  disabled,
}: {
  groups: DuplicateSourceGroup[];
  onMerge: (remotes: ProjectResource[]) => Promise<void>;
  disabled?: boolean;
}) {
  const { t } = useT("projects");
  const [merging, setMerging] = useState<string | null>(null);

  if (groups.length === 0) return null;

  return (
    <div className="space-y-2 rounded-md border border-warning/40 bg-warning/5 p-2">
      {groups.map((group) => (
        <div key={group.local.id} className="space-y-1.5">
          <div className="flex items-start gap-1.5">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warning" />
            <div className="min-w-0 space-y-0.5">
              <p className="text-caption font-medium">
                {t(($) => $.resources.duplicate_title, { repo: group.repoName })}
              </p>
              <p className="text-micro text-muted-foreground">
                {t(($) => $.resources.duplicate_body, {
                  // Deliberately not named `count`: i18next reads that key as
                  // a plural selector and would look up a `_one` / `_other`
                  // variant that does not exist.
                  remotes: String(group.remotes.length),
                })}
              </p>
            </div>
          </div>
          <div className="pl-5">
            <Button
              variant="outline"
              size="sm"
              className="h-6 px-2 text-micro"
              disabled={disabled || merging !== null}
              onClick={() => {
                setMerging(group.local.id);
                void onMerge(group.remotes).finally(() => setMerging(null));
              }}
            >
              {merging === group.local.id
                ? t(($) => $.resources.duplicate_merging)
                : t(($) => $.resources.duplicate_merge)}
            </Button>
          </div>
        </div>
      ))}
    </div>
  );
}
