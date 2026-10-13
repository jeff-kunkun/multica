"use client";

import { useState } from "react";
import { Globe, Lock } from "lucide-react";
import { ModelDropdown } from "./model-dropdown";
import { RuntimePicker } from "./runtime-picker";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import { InstructionsEditor } from "./instructions-editor";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import type {
  Agent,
  AgentVisibility,
  RuntimeDevice,
  MemberWithUser,
  CreateAgentRequest,
} from "@multica/core/types";
import { isImeComposing } from "@multica/core/utils";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { toast } from "sonner";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  VISIBILITY_DESCRIPTION,
  VISIBILITY_LABEL,
} from "@multica/core/agents";
import { CharCounter } from "./char-counter";
import { useT } from "../../i18n";

export function CreateAgentDialog({
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  template,
  onClose,
  onCreate,
}: {
  runtimes: RuntimeDevice[];
  runtimesLoading?: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  // When provided, the dialog opens in "Duplicate" mode: the visible
  // fields (name / description / runtime / visibility / model) are
  // pre-populated from this agent, and the hidden fields
  // (instructions / custom_args / custom_env / max_concurrent_tasks)
  // are forwarded to the create call so the new agent is a true clone.
  template?: Agent | null;
  onClose: () => void;
  onCreate: (data: CreateAgentRequest) => Promise<Agent | void>;
}) {
  const { t } = useT("agents");
  const isDuplicate = !!template;

  // Name defaults: duplicate uses "<original> copy". Manual-create starts blank.
  const [name, setName] = useState(
    template ? `${template.name}${t(($) => $.create_dialog.duplicate_copy_suffix)}` : "",
  );
  const [description, setDescription] = useState(template?.description ?? "");
  const [visibility, setVisibility] = useState<AgentVisibility>(
    template?.visibility ?? "workspace",
  );

  const [model, setModel] = useState(template?.model ?? "");
  const [instructions, setInstructions] = useState(template?.instructions ?? "");
  const [avatarUrl, setAvatarUrl] = useState<string | null>(template?.avatar_url ?? null);
  const [creating, setCreating] = useState(false);

  // Duplicate-mode pre-fill: clone lands on the source agent's runtime so
  // the user doesn't have to re-pick. Skipped when that runtime is now
  // locked for the caller (Create would 403). Empty fallback hands the
  // job to RuntimePicker — it owns filter state, so it's the only place
  // that knows which runtimes are visible right now.
  const [selectedRuntimeId, setSelectedRuntimeId] = useState(() => {
    const templateRuntime = template?.runtime_id
      ? runtimes.find((r) => r.id === template.runtime_id)
      : undefined;
    if (templateRuntime && isRuntimeUsableForUser(templateRuntime, currentUserId)) {
      return templateRuntime.id;
    }
    return "";
  });

  const selectedRuntime = runtimes.find((d) => d.id === selectedRuntimeId) ?? null;
  // Defense-in-depth: even if a locked runtime somehow ends up selected
  // (e.g. duplicate of an agent whose template runtime is now locked, and
  // the workspace has no usable fallback), gate Create on it so we don't
  // submit a request the backend will reject with 403.
  const selectedRuntimeLocked =
    selectedRuntime != null &&
    !isRuntimeUsableForUser(selectedRuntime, currentUserId);

  const handleSubmit = async () => {
    if (!name.trim() || !selectedRuntime || selectedRuntimeLocked) return;
    setCreating(true);

    try {
      const trimmedInstructions = instructions.trim();
      const data: CreateAgentRequest = {
        name: name.trim(),
        description: description.trim(),
        runtime_id: selectedRuntime.id,
        model: model.trim() || undefined,
        instructions: trimmedInstructions || undefined,
        avatar_url: avatarUrl ?? undefined,
        visibility,
      };
      if (template) {
        // Duplicate path: forward the hidden config fields the source
        // agent had so the clone is functional out of the box (args /
        // concurrency). As of
        // MUL-2600 the agent resource shape no longer carries
        // custom_env values, so duplication cannot copy env at all —
        // the user has to re-set env on the clone via the env tab
        // (which now goes through the audited `/env` endpoint). The
        // dialog's create call still accepts custom_env at create
        // time, but the source values aren't available here.
        if (template.custom_args.length) data.custom_args = template.custom_args;
        if (template.max_concurrent_tasks) {
          data.max_concurrent_tasks = template.max_concurrent_tasks;
        }
      }
      await onCreate(data);
      onClose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.create_dialog.create_failed_toast));
      setCreating(false);
    }
  };

  const headerTitle = isDuplicate
    ? t(($) => $.create_dialog.title_duplicate)
    : t(($) => $.create_dialog.title_create);

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="p-0 gap-0 flex flex-col overflow-hidden !top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2 !w-full !max-w-2xl !h-[85vh]">
        <DialogHeader className="border-b px-5 py-3 space-y-0">
          <DialogTitle className="text-title-sm font-semibold">{headerTitle}</DialogTitle>
          {isDuplicate && template && (
            <DialogDescription className="mt-1 text-caption">
              {t(($) => $.create_dialog.description_duplicate, { name: template.name })}
            </DialogDescription>
          )}
        </DialogHeader>

        <div className="flex-1 overflow-y-auto p-5">
          <div className="space-y-4 min-w-0">
            {/* Identity row: avatar (left) + name & description stack
                (right). The avatar visually anchors the identity of
                what the user is creating; pairing it with the Name
                field reads as "this is the agent's face + name",
                same shape as detail-page header so the affordance is
                instantly familiar. */}
            <div className="flex items-start gap-4">
              <AvatarUploadControl
                variant="agent"
                value={avatarUrl}
                name={name}
                size={64}
                onUploaded={setAvatarUrl}
                onEmojiSelected={setAvatarUrl}
                onClear={() => setAvatarUrl(null)}
              />
              <div className="flex-1 min-w-0 space-y-3">
                <div>
                  <Label className="text-caption text-muted-foreground">{t(($) => $.create_dialog.name_label)}</Label>
                  <Input
                    autoFocus
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t(($) => $.create_dialog.name_placeholder)}
                    className="mt-1"
                    onKeyDown={(e) => {
                      if (isImeComposing(e)) return;
                      if (e.key === "Enter") handleSubmit();
                    }}
                  />
                </div>

                <div>
                  <Label className="text-caption text-muted-foreground">{t(($) => $.create_dialog.description_label)}</Label>
                  <Input
                    type="text"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder={t(($) => $.create_dialog.description_placeholder)}
                    maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                    className="mt-1"
                  />
                  <div className="mt-1">
                    <CharCounter
                      length={[...description].length}
                      max={AGENT_DESCRIPTION_MAX_LENGTH}
                    />
                  </div>
                </div>
              </div>
            </div>

            <div>
              <Label className="text-caption text-muted-foreground">{t(($) => $.create_dialog.visibility_label)}</Label>
              <div className="mt-1.5 flex gap-2">
                <button
                  type="button"
                  onClick={() => setVisibility("workspace")}
                  className={`flex flex-1 items-center gap-2 rounded-lg border px-3 py-2.5 text-body transition-colors ${
                    visibility === "workspace"
                      ? "border-primary bg-primary/5"
                      : "border-border hover:bg-muted"
                  }`}
                >
                  <Globe className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <div className="text-left">
                    <div className="font-medium">{VISIBILITY_LABEL.workspace}</div>
                    <div className="text-caption text-muted-foreground">
                      {VISIBILITY_DESCRIPTION.workspace}
                    </div>
                  </div>
                </button>
                <button
                  type="button"
                  onClick={() => setVisibility("private")}
                  className={`flex flex-1 items-center gap-2 rounded-lg border px-3 py-2.5 text-body transition-colors ${
                    visibility === "private"
                      ? "border-primary bg-primary/5"
                      : "border-border hover:bg-muted"
                  }`}
                >
                  <Lock className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <div className="text-left">
                    <div className="font-medium">{VISIBILITY_LABEL.private}</div>
                    <div className="text-caption text-muted-foreground">
                      {VISIBILITY_DESCRIPTION.private}
                    </div>
                  </div>
                </button>
              </div>
            </div>

            <RuntimePicker
              runtimes={runtimes}
              runtimesLoading={runtimesLoading}
              members={members}
              currentUserId={currentUserId}
              selectedRuntimeId={selectedRuntimeId}
              onSelect={(id) => {
                // Models are per-runtime; a value picked for the old runtime
                // may not exist on the new one, so drop it on runtime change.
                if (id !== selectedRuntimeId) setModel("");
                setSelectedRuntimeId(id);
              }}
            />

            <ModelDropdown
              runtimeId={selectedRuntime?.id ?? null}
              runtimeOnline={selectedRuntime?.status === "online"}
              value={model}
              onChange={setModel}
              disabled={!selectedRuntime}
            />

            {/* --- Optional section (instructions) ---
                Collapsed by default so quick-create stays fast.
                Duplicate pre-fills everything from the source agent. */}
            <InstructionsEditor
              value={instructions}
              onChange={setInstructions}
              placeholder={
                isDuplicate
                  ? t(($) => $.create_dialog.instructions.placeholder_duplicate)
                  : t(($) => $.create_dialog.instructions.placeholder_blank)
              }
            />
          </div>
        </div>

        {/* Inline footer instead of <DialogFooter>: the shipped
            DialogFooter applies `-mx-4 -mb-4` assuming a padded
            DialogContent (default `p-4`). Our DialogContent uses
            `p-0`, so those negative margins push the footer outside
            the dialog. A plain flex row anchored by `border-t` keeps
            the visual rhythm without the overflow bug. */}
        <div className="flex items-center justify-end gap-2 border-t bg-background px-5 py-3">
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.create_dialog.cancel)}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={
              creating || !name.trim() || !selectedRuntime || selectedRuntimeLocked
            }
            title={
              selectedRuntimeLocked
                ? t(($) => $.create_dialog.runtime_private_locked_tooltip)
                : undefined
            }
          >
            {creating ? t(($) => $.create_dialog.creating) : t(($) => $.create_dialog.create)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
