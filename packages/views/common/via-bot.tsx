"use client";

import { useT } from "../i18n";

/** Historical bot attribution supplied by the authenticated server write. */
export function ViaBot({ name }: { name?: string | null }) {
  const { t } = useT("common");
  if (!name) return null;
  return <span className="block min-w-0 text-caption font-normal text-muted-foreground [overflow-wrap:anywhere]">{t(($) => $.via_bot, { name })}</span>;
}
