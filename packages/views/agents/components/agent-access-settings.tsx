"use client";

import type { Agent, MemberWithUser } from "@multica/core/types";
import {
  SettingsCard,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { InheritedConfigNotice } from "./inherited-config-notice";
import { AccessPicker } from "./inspector/access-picker";
import { isSpecialization } from "../specialization";

export function AgentAccessSettings({
  agent,
  members,
  currentUserId,
  onDirtyChange,
  onUpdate,
  parentAgent,
}: {
  agent: Agent;
  members: MemberWithUser[];
  currentUserId: string | null;
  onDirtyChange?: (dirty: boolean) => void;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
  /**
   * The base-role row when the caller holds it. A specialisation inherits the
   * base role's access rule, so the picker would write something the server
   * refuses (DENE-470); the notice names where it is edited instead.
   */
  parentAgent?: Agent | null;
}) {
  const { t } = useT("agents");

  if (isSpecialization(agent)) {
    return <InheritedConfigNotice agent={agent} parentAgent={parentAgent} />;
  }

  return (
    <SettingsSection
      title={t(($) => $.access.section_title)}
      description={t(($) => $.inspector.section_access_hint)}
    >
      <SettingsCard>
        <AccessPicker
          permissionMode={agent.permission_mode}
          invocationTargets={agent.invocation_targets}
          visibility={agent.visibility}
          members={members}
          ownerId={agent.owner_id}
          canEdit={
            currentUserId !== null && agent.owner_id === currentUserId
          }
          hasComposioAllowlist={
            (agent.composio_toolkit_allowlist ?? []).length > 0
          }
          onDirtyChange={onDirtyChange}
          onChange={(next) => onUpdate(agent.id, next)}
        />
      </SettingsCard>
    </SettingsSection>
  );
}
