/**
 * Status picker route for an existing issue — presented as a formSheet
 * (UISheetPresentationController) by the parent Stack.
 *
 * Self-contained: reads the issue from the TanStack Query detail cache,
 * calls `useUpdateIssue` directly on selection, then `router.back()`s. No
 * onChange callback to a parent.
 *
 * If the cache is cold (rare — the user reaches this screen by tapping
 * a chip on the issue-detail page that already populated it), the picker
 * still renders against the current value of `todo` and the optimistic
 * mutation patches the cache when the user picks.
 */
import { useLocalSearchParams, router } from "expo-router";
import { Alert } from "react-native";
import { useT } from "@/lib/i18n";
import { api } from "@/data/api";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { StatusPickerBody } from "@/components/issue/pickers/status-picker-body";
import { issueDetailOptions } from "@/data/queries/issues";
import { useUpdateIssue } from "@/data/mutations/issues";
import { useWorkspaceStore } from "@/data/workspace-store";

export default function IssueStatusPickerRoute() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { data: issue } = useQuery(issueDetailOptions(wsId, id));
  const updateIssue = useUpdateIssue(id);
  const { t } = useT("issues");
  const qc = useQueryClient();
  const cancelIssue = useMutation({
    mutationFn: (related: boolean) => api.updateIssue(id, {
      status: "cancelled",
      ...(related ? { related_stop: true } : {}),
    }),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["issues", wsId] }),
        qc.invalidateQueries({ queryKey: ["chat"] }),
      ]);
      router.back();
    },
    onError: (error) => Alert.alert(t("cancel_work.title"), error instanceof Error ? error.message : t("cancel_work.failed")),
  });

  return (
    <StatusPickerBody
      value={issue?.status ?? "todo"}
      onChange={(next) => {
        if (cancelIssue.isPending || updateIssue.isPending) return;
        if (next === "cancelled") {
          Alert.alert(t("cancel_work.title"), t("cancel_work.description"), [
            { text: t("cancel_work.keep"), style: "cancel" },
            { text: t("cancel_work.confirm"), style: "destructive", onPress: () => cancelIssue.mutate(false) },
            { text: t("cancel_work.confirm_related"), style: "destructive", onPress: () => cancelIssue.mutate(true) },
          ]);
          return;
        }
        updateIssue.mutate({ status: next }, { onSuccess: () => router.back() });
      }}
    />
  );
}
