export interface SettingsLocation {
  tab: string;
  /** In-page anchor to scroll to (see `SettingsSection`/`SettingsRow`). */
  section: string | null;
}

/**
 * Resolve a settings URL, including links that predate the current layout.
 * Server redirects still point at some of them — the GitHub App install flow
 * returns to `?tab=github` / `?tab=repositories` — so these mappings are part
 * of the URL contract, not bookmarks we can drop.
 */
export function resolveSettingsLocation(
  params: URLSearchParams,
): SettingsLocation {
  const tab = params.get("tab") ?? "profile";
  const section = params.get("section");
  switch (tab) {
    case "issue":
    case "chat":
      return { tab: "preferences", section: tab };
    case "labs":
      return { tab: "workspace", section: null };
    case "github":
      return { tab: "code", section };
    case "repositories":
      return { tab: "code", section: section ?? "repositories" };
    case "integrations":
      return {
        tab: "code",
        section: params.get("integration") === "vcs" ? "code-hosting" : null,
      };
    default:
      return { tab, section };
  }
}

/** Clear page-specific state while preserving unrelated callback parameters. */
export function settingsHref(
  pathname: string,
  searchParams: URLSearchParams,
  tab: string,
  detail: { section?: string } = {},
) {
  const params = new URLSearchParams(searchParams);
  params.set("tab", tab);
  params.delete("section");
  if (detail.section) params.set("section", detail.section);
  return `${pathname}?${params.toString()}`;
}
