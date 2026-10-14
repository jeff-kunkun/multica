"use client";

import type { AgentRuntime } from "@multica/core/types";
import { readRuntimeMagpieRoute } from "@multica/core/runtimes";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { runtimeCliLabel } from "../../runtimes/components/runtime-machines";
import { useT } from "../../i18n";
import { MagpieRouteLine } from "./magpie-route-line";

/**
 * Read-only settings: the office mirrors magpie, so this shows the CLI and
 * the route the daemon reported, and nothing here can be edited.
 */
export function AgentDetailInspector({
  runtime,
}: {
  runtime: AgentRuntime | null;
}) {
  const { t } = useT("agents");
  // A machine without magpie reports no route; the CLI picks its own model.
  const route = readRuntimeMagpieRoute(runtime?.metadata) ?? {
    group: "",
    models: [],
  };

  return (
    <SettingsSection
      title={t(($) => $.inspector.section_execution)}
      description={t(($) => $.magpie.edit_hint)}
    >
      <SettingsCard>
        <SettingsRow label={t(($) => $.inspector.prop_cli)}>
          <span className="text-body">
            {runtime
              ? runtimeCliLabel(runtime)
              : t(($) => $.pickers.runtime_none)}
          </span>
        </SettingsRow>
        <SettingsRow label={t(($) => $.inspector.prop_model)}>
          <MagpieRouteLine route={route} />
        </SettingsRow>
      </SettingsCard>
    </SettingsSection>
  );
}
