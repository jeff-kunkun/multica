/**
 * Inline notice rendered above the chat input when the active agent's
 * runtime isn't reachable. Mirror of
 * `packages/views/chat/components/offline-banner.tsx`.
 *
 * Both offline states show last contact without disabling the composer.
 *
 * Loading silence: `undefined` and the implicit "online" case render nothing.
 * The chat composer never sees a speculative offline flash during the cold
 * fetch window — copy only appears when there's a real-world implication for
 * the message the user is about to send.
 */
import { Ionicons } from "@expo/vector-icons";
import { View } from "react-native";
import type { AgentAvailability } from "@multica/core/agents";
import { Text } from "@/components/ui/text";
import { useT } from "@/lib/i18n";

interface Props {
  /** Display name for the copy. */
  agentName?: string;
  lastSeenAt?: string | null;
  /**
   * Resolved presence availability. Pass `undefined` to suppress the banner
   * — we only surface known offline / unstable states, never speculative
   * copy during loading.
   */
  availability: AgentAvailability | undefined;
}

export function OfflineBanner({ availability, lastSeenAt }: Props) {
  const { t } = useT("chat");
  if (availability !== "offline" && availability !== "unstable") return null;
  const date = lastSeenAt ? new Date(lastSeenAt) : null;
  const time = date && Number.isFinite(date.getTime())
    ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })
    : null;
  return (
    <View accessibilityRole="text" className="mx-3 mb-1.5 flex-row items-center gap-1.5 px-2.5 py-1.5">
      <Ionicons name="cloud-offline-outline" size={14} color="#71717a" />
      <Text className="flex-1 text-xs text-muted-foreground">
        {time ? t("banners.last_seen", { time }) : t("banners.last_seen_unknown")}
      </Text>
    </View>
  );
}
