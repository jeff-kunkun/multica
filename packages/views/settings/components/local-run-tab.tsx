"use client";

import { useWorkspacePaths } from "@multica/core/paths";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { useFullDiskAccess } from "../../platform";
import { SettingsCard, SettingsRow, SettingsTab } from "./settings-layout";

/** Entry point to the machines that run agents; the runtimes page owns the detail. */
export function LocalRunTab() {
  const { t } = useT("office");
  const p = useWorkspacePaths();
  const { access, openSettings } = useFullDiskAccess();
  return (
    <SettingsTab title={t(($) => $.local_run.title)} description={t(($) => $.local_run.description)}>
      <SettingsCard>
        {access.status === "missing" && (
          <SettingsRow
            label={t(($) => $.local_run.disk_access)}
            description={t(($) => $.local_run.disk_access_description, {
              app: access.appName,
            })}
          >
            <Button variant="outline" size="sm" onClick={openSettings}>
              {t(($) => $.local_run.disk_access_open)}
            </Button>
          </SettingsRow>
        )}
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
