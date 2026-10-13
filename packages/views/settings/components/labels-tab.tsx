"use client";

import { useT } from "../../i18n";
import { LabelManager } from "../../labels/label-manager";
import { SettingsTab } from "./settings-layout";

/**
 * The workspace's issue label catalog. Agent labels were removed from the
 * product (MUL-5600) even though the backend still models the `agent`
 * resource type.
 */
export function LabelsTab() {
  const { t } = useT("settings");

  return (
    <SettingsTab title={t(($) => $.labels.title)} scope="workspace">
      <LabelManager scope="issue" />
    </SettingsTab>
  );
}
