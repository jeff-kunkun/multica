import type { ChatLinkedProject, ChatLinkedProjectRef, LinkedProjectOption } from "@multica/core/types";

interface LinkedRow {
  key: string;
  ref: ChatLinkedProjectRef;
  title: string;
  icon: string | null;
  sourceName: string;
  available: boolean;
}

export function linkedKey(ref: ChatLinkedProjectRef): string {
  return `${ref.link_id}/${ref.project_id}`;
}

/** Every shared project on offer, then attached ones the link no longer
 *  offers — those stay listed (ticked, marked stale) until removed. */
export function linkedRows(options: LinkedProjectOption[], attached: ChatLinkedProject[]): LinkedRow[] {
  const rows: LinkedRow[] = options.map((option) => ({
    key: linkedKey({ link_id: option.link_id, project_id: option.id }),
    ref: { link_id: option.link_id, project_id: option.id },
    title: option.title,
    icon: option.icon,
    sourceName: option.source.name,
    available: true,
  }));
  const offered = new Set(rows.map((row) => row.key));
  for (const item of attached) {
    const key = linkedKey(item);
    if (offered.has(key)) continue;
    rows.push({
      key,
      ref: { link_id: item.link_id, project_id: item.project_id },
      title: item.title,
      icon: item.icon,
      sourceName: item.source_name,
      available: false,
    });
  }
  return rows;
}
