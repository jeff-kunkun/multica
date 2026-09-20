"use client";

// The "providers" half of the agent accounts tab (DENE-348).
//
// The account half above it answers "which CLI configuration DIRECTORY is in
// effect". This one answers a different question — "which endpoint and key
// does the CLI send requests to" — and the two are deliberately siblings
// rather than merged: a user can keep one account directory and swap the
// route under it, or keep the route and swap accounts.
//
// Nothing here is optimistic. All four actions write files on the user's own
// machine, they fail for ordinary reasons (a settings file another process is
// holding, an unparseable credentials file), the user stays on this screen,
// and the receipt carries the machine's real state — so there is nothing worth
// predicting and a wrong guess would draw a configuration the machine does not
// have. Every button waits for the daemon and redraws from its answer.
//
// The key is write-only end to end: the daemon reports a mask, no type in this
// tree has a field for a key value, and the input starts empty and stays empty
// unless the user types. `provider-presets-model.ts` holds the rules and their
// canonical tests.

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, KeyRound, Loader2, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  runtimeProviderPresetsKeys,
  runtimeProviderPresetsOptions,
  useProviderPresetMutation,
  PROVIDER_PRESET_APIS,
} from "@multica/core/runtimes";
import type { RuntimeDevice, RuntimeProviderPreset } from "@multica/core/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@multica/ui/components/ui/field";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import {
  type ProviderPresetFieldError,
  type ProviderPresetForm,
  canManageProviderPresets,
  emptyProviderPresetForm,
  providerPresetFormFrom,
  providerPresetKeyState,
  providerPresetSummaryLine,
  providerPresetUpsertInput,
  providerPresetsViewState,
  supportsProviderPresets,
  validateProviderPresetForm,
} from "./provider-presets-model";

export interface AgentProviderPresetsSectionProps {
  runtimeDevice?: RuntimeDevice;
}

/**
 * Renders nothing unless the agent's runtime runs a CLI the daemon has a
 * preset driver for. Not a disabled block — see `supportsProviderPresets`.
 */
export function AgentProviderPresetsSection({
  runtimeDevice,
}: AgentProviderPresetsSectionProps) {
  if (!runtimeDevice || !supportsProviderPresets(runtimeDevice.provider)) {
    return null;
  }
  return <ProviderPresets runtimeId={runtimeDevice.id} />;
}

function ProviderPresets({ runtimeId }: { runtimeId: string }) {
  const { t } = useT("agents");
  const queryClient = useQueryClient();
  const query = useQuery(runtimeProviderPresetsOptions(runtimeId));
  const mutation = useProviderPresetMutation(runtimeId);

  const [editing, setEditing] = useState<ProviderPresetForm | null>(null);
  const [pendingDelete, setPendingDelete] = useState<RuntimeProviderPreset | null>(
    null,
  );
  // Which row's activate is in flight, so only that button shows a spinner
  // instead of every row going busy at once.
  const [activatingId, setActivatingId] = useState<string | null>(null);

  const state = useMemo(
    () =>
      providerPresetsViewState({
        presets: query.data?.presets,
        loading: query.isPending,
        error: query.isError
          ? query.error instanceof Error && query.error.message
            ? query.error.message
            : t(($) => $.tab_body.providers.error_description)
          : "",
      }),
    [query.data, query.isPending, query.isError, query.error, t],
  );

  const manageable = canManageProviderPresets(state);

  const handleActivate = async (preset: RuntimeProviderPreset) => {
    setActivatingId(preset.id);
    try {
      await mutation.mutateAsync({ action: "activate", id: preset.id });
      toast.success(t(($) => $.tab_body.providers.activated_toast, { name: preset.id }));
    } catch (err) {
      toast.error(errorText(err, t(($) => $.tab_body.providers.activate_failed_toast)));
    } finally {
      setActivatingId(null);
    }
  };

  const handleDelete = async (preset: RuntimeProviderPreset) => {
    try {
      const result = await mutation.mutateAsync({ action: "delete", id: preset.id });
      // `cleared_active` is the daemon telling us the machine now has no
      // default model at all. That is a consequence the user has to be told
      // about out loud, not something to infer from a row disappearing.
      toast.success(
        result.clearedActive
          ? t(($) => $.tab_body.providers.deleted_cleared_toast, { name: preset.id })
          : t(($) => $.tab_body.providers.deleted_toast, { name: preset.id }),
      );
    } catch (err) {
      toast.error(errorText(err, t(($) => $.tab_body.providers.delete_failed_toast)));
    } finally {
      setPendingDelete(null);
    }
  };

  const handleSubmit = async (form: ProviderPresetForm) => {
    try {
      await mutation.mutateAsync({
        action: "upsert",
        preset: providerPresetUpsertInput(form),
      });
      toast.success(
        form.editingId
          ? t(($) => $.tab_body.providers.updated_toast, { name: form.id.trim() })
          : t(($) => $.tab_body.providers.created_toast, { name: form.id.trim() }),
      );
      setEditing(null);
    } catch (err) {
      toast.error(errorText(err, t(($) => $.tab_body.providers.save_failed_toast)));
    }
  };

  const handleRetry = () => {
    void queryClient.invalidateQueries({
      queryKey: runtimeProviderPresetsKeys.forRuntime(runtimeId),
    });
  };

  return (
    <section className="space-y-3" aria-label={t(($) => $.tab_body.providers.title)}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1">
          <h3 className="text-body font-medium">
            {t(($) => $.tab_body.providers.title)}
          </h3>
          <p className="mt-0.5 max-w-2xl text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.providers.intro)}
          </p>
        </div>
        {manageable ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="shrink-0"
            onClick={() => setEditing(emptyProviderPresetForm())}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.providers.add_action)}
          </Button>
        ) : null}
      </div>

      {state.kind === "loading" ? <ProviderPresetsLoading /> : null}

      {state.kind === "error" ? (
        <div className="rounded-lg border border-destructive/30 bg-background p-5 text-center">
          <span className="mx-auto flex size-9 items-center justify-center rounded-lg bg-destructive/10 text-destructive">
            <AlertTriangle className="size-4" aria-hidden="true" />
          </span>
          <p className="mt-3 text-body font-medium">
            {t(($) => $.tab_body.providers.error_title)}
          </p>
          <p className="mx-auto mt-1 max-w-lg text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.providers.error_description)}
          </p>
          <p
            className="mx-auto mt-2 max-w-lg truncate font-mono text-micro text-muted-foreground"
            translate="no"
          >
            {state.message}
          </p>
          <Button type="button" size="sm" className="mt-4" onClick={handleRetry}>
            {t(($) => $.tab_body.providers.error_retry_action)}
          </Button>
        </div>
      ) : null}

      {state.kind === "empty" ? (
        <div className="rounded-lg border border-border bg-background p-6 text-center">
          <span className="mx-auto flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            <KeyRound className="size-4" aria-hidden="true" />
          </span>
          <p className="mt-3 text-body font-medium">
            {t(($) => $.tab_body.providers.empty_title)}
          </p>
          <p className="mx-auto mt-1 max-w-lg text-pretty text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.providers.empty_description)}
          </p>
        </div>
      ) : null}

      {state.kind === "ready" ? (
        <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
          {state.presets.map((preset) => (
            <ProviderPresetRow
              key={preset.id}
              preset={preset}
              busy={mutation.isPending}
              activating={activatingId === preset.id}
              onActivate={() => void handleActivate(preset)}
              onEdit={() => setEditing(providerPresetFormFrom(preset))}
              onDelete={() => setPendingDelete(preset)}
            />
          ))}
        </ul>
      ) : null}

      {editing ? (
        <ProviderPresetDialog
          form={editing}
          saving={mutation.isPending}
          onChange={setEditing}
          onClose={() => setEditing(null)}
          onSubmit={() => void handleSubmit(editing)}
        />
      ) : null}

      {pendingDelete ? (
        <DeletePresetDialog
          preset={pendingDelete}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => void handleDelete(pendingDelete)}
        />
      ) : null}
    </section>
  );
}

function ProviderPresetRow({
  preset,
  busy,
  activating,
  onActivate,
  onEdit,
  onDelete,
}: {
  preset: RuntimeProviderPreset;
  busy: boolean;
  activating: boolean;
  onActivate: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useT("agents");
  const active = preset.active === true;
  const keyState = providerPresetKeyState(preset);

  return (
    <li
      data-active={active ? "true" : undefined}
      className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 p-3.5 transition-colors hover:bg-muted/50"
    >
      <div className="min-w-0 flex-1 basis-56">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          {/* The active row is marked by weight and text colour, which hover
              does not touch, plus a pill — so selecting stays legible while
              the pointer is on the row. */}
          <span
            className={cn(
              "min-w-0 truncate text-caption",
              active ? "font-semibold text-foreground" : "font-medium",
            )}
            translate="no"
          >
            {preset.id}
          </span>
          {active ? (
            <span className="inline-flex shrink-0 items-center rounded-full bg-brand/10 px-2 py-0.5 text-micro font-medium text-brand">
              {t(($) => $.tab_body.providers.active_badge)}
            </span>
          ) : null}
        </div>
        <p
          className="mt-0.5 truncate font-mono text-micro text-muted-foreground"
          translate="no"
        >
          {providerPresetSummaryLine(preset)}
        </p>
        <p className="mt-0.5 truncate text-micro text-muted-foreground">
          {keyState === "masked"
            ? t(($) => $.tab_body.providers.key_masked, { mask: preset.key_mask ?? "" })
            : keyState === "stored"
              ? t(($) => $.tab_body.providers.key_stored)
              : t(($) => $.tab_body.providers.key_absent)}
          {preset.models.length > 0
            ? ` · ${t(($) => $.tab_body.providers.model_count, { count: preset.models.length })}`
            : ""}
        </p>
      </div>
      <div className="ms-auto flex shrink-0 items-center gap-2">
        {active ? null : (
          <Button type="button" size="sm" disabled={busy} onClick={onActivate}>
            {activating ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            ) : null}
            {t(($) => $.tab_body.providers.activate_action)}
          </Button>
        )}
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={onEdit}
        >
          {t(($) => $.tab_body.providers.edit_action)}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={onDelete}
          aria-label={t(($) => $.tab_body.providers.delete_aria, { name: preset.id })}
        >
          <Trash2 className="size-3.5" aria-hidden="true" />
        </Button>
      </div>
    </li>
  );
}

function ProviderPresetDialog({
  form,
  saving,
  onChange,
  onClose,
  onSubmit,
}: {
  form: ProviderPresetForm;
  saving: boolean;
  onChange: (next: ProviderPresetForm) => void;
  onClose: () => void;
  onSubmit: () => void;
}) {
  const { t } = useT("agents");
  const [showErrors, setShowErrors] = useState(false);
  const errors = validateProviderPresetForm(form);
  const editing = form.editingId !== "";

  const set = <K extends keyof ProviderPresetForm>(
    key: K,
    value: ProviderPresetForm[K],
  ) => onChange({ ...form, [key]: value });

  const setModel = (index: number, patch: { id?: string; name?: string }) =>
    onChange({
      ...form,
      models: form.models.map((model, i) =>
        i === index ? { ...model, ...patch } : model,
      ),
    });

  const has = (error: ProviderPresetFieldError) =>
    showErrors && errors.includes(error);

  const handleSubmit = () => {
    if (errors.length > 0) {
      setShowErrors(true);
      return;
    }
    onSubmit();
  };

  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {editing
              ? t(($) => $.tab_body.providers.form_title_edit)
              : t(($) => $.tab_body.providers.form_title_add)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.tab_body.providers.form_description)}
          </DialogDescription>
        </DialogHeader>

        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="preset-id">
              {t(($) => $.tab_body.providers.field_id)}
            </FieldLabel>
            <Input
              id="preset-id"
              value={form.id}
              // The id is the preset's identity in the daemon's files, so an
              // edit keeps it fixed: renaming would be an add plus a delete,
              // and doing that silently could strand the active pointer.
              disabled={editing}
              onChange={(event) => set("id", event.target.value)}
              // eslint-disable-next-line no-restricted-syntax -- sample id, not prose: it shows the allowed character set, which is the same in every locale.
              placeholder="my-provider"
              aria-invalid={has("id_required") || has("id_invalid")}
            />
            <FieldDescription>
              {t(($) => $.tab_body.providers.field_id_hint)}
            </FieldDescription>
            {has("id_required") ? (
              <FieldError>{t(($) => $.tab_body.providers.error_id_required)}</FieldError>
            ) : null}
            {has("id_invalid") ? (
              <FieldError>{t(($) => $.tab_body.providers.error_id_invalid)}</FieldError>
            ) : null}
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-base-url">
              {t(($) => $.tab_body.providers.field_base_url)}
            </FieldLabel>
            <Input
              id="preset-base-url"
              value={form.baseUrl}
              onChange={(event) => set("baseUrl", event.target.value)}
              placeholder="https://api.example.com/v1"
              aria-invalid={has("base_url_required") || has("base_url_invalid")}
            />
            {has("base_url_required") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_base_url_required)}
              </FieldError>
            ) : null}
            {has("base_url_invalid") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_base_url_invalid)}
              </FieldError>
            ) : null}
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-api">
              {t(($) => $.tab_body.providers.field_api)}
            </FieldLabel>
            <Select
              items={PROVIDER_PRESET_APIS.map((api) => ({ value: api, label: api }))}
              value={form.api}
              onValueChange={(value) => value && set("api", value)}
            >
              <SelectTrigger id="preset-api">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PROVIDER_PRESET_APIS.map((api) => (
                  <SelectItem key={api} value={api}>
                    {api}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-api-key">
              {t(($) => $.tab_body.providers.field_api_key)}
            </FieldLabel>
            {/* Write-only. The value box starts empty and is never seeded from
                the mask — the stored credential's state is reported beside it
                as text instead, so a submit without typing carries no key. */}
            <Input
              id="preset-api-key"
              type="password"
              autoComplete="off"
              value={form.apiKey}
              onChange={(event) => set("apiKey", event.target.value)}
              placeholder={t(($) => $.tab_body.providers.field_api_key_placeholder)}
            />
            <FieldDescription>
              {form.hasKey
                ? form.keyMask
                  ? t(($) => $.tab_body.providers.field_api_key_hint_masked, {
                      mask: form.keyMask,
                    })
                  : t(($) => $.tab_body.providers.field_api_key_hint_stored)
                : t(($) => $.tab_body.providers.field_api_key_hint_absent)}
            </FieldDescription>
          </Field>

          <Field>
            <FieldLabel htmlFor="preset-api-key-env">
              {t(($) => $.tab_body.providers.field_api_key_env)}
            </FieldLabel>
            <Input
              id="preset-api-key-env"
              value={form.apiKeyEnv}
              onChange={(event) => set("apiKeyEnv", event.target.value)}
              // eslint-disable-next-line no-restricted-syntax -- sample environment variable name, not prose: shell variable names are not translated.
              placeholder="MY_PROVIDER_API_KEY"
              aria-invalid={has("api_key_env_invalid")}
            />
            <FieldDescription>
              {t(($) => $.tab_body.providers.field_api_key_env_hint)}
            </FieldDescription>
            {has("api_key_env_invalid") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_api_key_env_invalid)}
              </FieldError>
            ) : null}
          </Field>

          <Field>
            <FieldLabel>{t(($) => $.tab_body.providers.field_models)}</FieldLabel>
            <div className="space-y-2">
              {form.models.map((model, index) => (
                <div key={index} className="flex flex-wrap items-center gap-2">
                  <Input
                    className="min-w-0 flex-1 basis-48"
                    value={model.id}
                    onChange={(event) => setModel(index, { id: event.target.value })}
                    placeholder={t(($) => $.tab_body.providers.field_model_id)}
                    aria-label={t(($) => $.tab_body.providers.field_model_id_aria, {
                      n: index + 1,
                    })}
                  />
                  <Input
                    className="min-w-0 flex-1 basis-40"
                    value={model.name}
                    onChange={(event) => setModel(index, { name: event.target.value })}
                    placeholder={t(($) => $.tab_body.providers.field_model_name)}
                    aria-label={t(($) => $.tab_body.providers.field_model_name_aria, {
                      n: index + 1,
                    })}
                  />
                  {form.models.length > 1 ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      aria-label={t(($) => $.tab_body.providers.remove_model_aria, {
                        n: index + 1,
                      })}
                      onClick={() =>
                        onChange({
                          ...form,
                          models: form.models.filter((_, i) => i !== index),
                        })
                      }
                    >
                      <Trash2 className="size-3.5" aria-hidden="true" />
                    </Button>
                  ) : null}
                </div>
              ))}
            </div>
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="mt-1 self-start"
              onClick={() =>
                onChange({ ...form, models: [...form.models, { id: "", name: "" }] })
              }
            >
              <Plus className="size-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.providers.add_model_action)}
            </Button>
            {has("models_required") ? (
              <FieldError>
                {t(($) => $.tab_body.providers.error_models_required)}
              </FieldError>
            ) : null}
          </Field>
        </FieldGroup>

        <DialogFooter>
          <Button type="button" variant="outline" disabled={saving} onClick={onClose}>
            {t(($) => $.tab_body.providers.cancel_action)}
          </Button>
          <Button type="button" disabled={saving} onClick={handleSubmit}>
            {saving ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            ) : null}
            {t(($) => $.tab_body.providers.save_action)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeletePresetDialog({
  preset,
  onCancel,
  onConfirm,
}: {
  preset: RuntimeProviderPreset;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useT("agents");
  const active = preset.active === true;

  return (
    <AlertDialog open onOpenChange={(open) => (open ? undefined : onCancel())}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t(($) => $.tab_body.providers.delete_title, { name: preset.id })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {/* Deleting the preset in effect leaves the CLI with no default
                model at all, which the user has to be told BEFORE the click,
                not discover afterwards from a failing run. */}
            {active
              ? t(($) => $.tab_body.providers.delete_active_description)
              : t(($) => $.tab_body.providers.delete_description)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>
            {t(($) => $.tab_body.providers.cancel_action)}
          </AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>
            {t(($) => $.tab_body.providers.delete_confirm_action)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function ProviderPresetsLoading() {
  const { t } = useT("agents");
  return (
    <div className="rounded-lg border border-border bg-background p-4">
      <div className="space-y-3">
        {[0, 1].map((index) => (
          <div key={index} className="flex items-center gap-3">
            <div className="min-w-0 flex-1 space-y-1.5">
              <Skeleton className="h-3 w-1/3" />
              <Skeleton className="h-2.5 w-1/2" />
            </div>
            <Skeleton className="h-7 w-16 shrink-0 rounded-md" />
          </div>
        ))}
      </div>
      <p className="mt-3 text-caption text-muted-foreground">
        {t(($) => $.tab_body.providers.loading_text)}
      </p>
    </div>
  );
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}
