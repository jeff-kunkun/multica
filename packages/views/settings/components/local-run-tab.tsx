"use client";

import { useWorkspacePaths } from "@multica/core/paths";
import { buttonVariants } from "@multica/ui/components/ui/button";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "./settings-layout";

/** Entry point to the machines that run agents; the runtimes page owns the detail. */
export function LocalRunTab() {
  const { t } = useT("office");
  const p = useWorkspacePaths();
  return (
    <SettingsTab title={t(($) => $.local_run.title)} description={t(($) => $.local_run.description)}>
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.local_run.runtimes)}
          description={t(($) => $.local_run.runtimes_description)}
        >
          <AppLink
            href={p.runtimes()}
            className={buttonVariants({ variant: "outline", size: "sm" })}
          >
            {t(($) => $.local_run.open)}
          </AppLink>
        </SettingsRow>
      </SettingsCard>
    </SettingsTab>
  );
}
