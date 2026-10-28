const MAX_TITLE = 80;

/**
 * A sub-issue title read off a comment (DENE-2671): its first line of text,
 * without markdown markers or link targets, at most 80 characters. Empty when
 * the comment has no text, so the form opens untitled.
 */
export function commentSubIssueTitle(content: string): string {
  for (const raw of content.split("\n")) {
    const line = raw
      .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/[*_`~]/g, "")
      .replace(/^\s*(?:#+|>|[-+]|\d+[.)])\s*/, "")
      .trim();
    if (!line) continue;
    const chars = Array.from(line);
    return chars.length > MAX_TITLE ? `${chars.slice(0, MAX_TITLE - 1).join("").trimEnd()}…` : line;
  }
  return "";
}
