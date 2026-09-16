import type { ChatMessage, IssueDraftPayload } from "../types";

/**
 * Wire format between the alignment page and the hidden `issue_draft:*` carrier.
 *
 * The carrier's instructions are fixed server-side (issueDraftInstructions in
 * server/internal/handler/issue_draft.go) and mandate exactly one
 * `<issue_draft>{...}</issue_draft>` block at the end of every reply, so the
 * block is a contract, not a rendering detail: it has to be parsed into the
 * preview and stripped before the message reaches the screen.
 *
 * Outbound is the mirror image. The instructions say to "preserve good existing
 * draft fields supplied in the user's message", so each turn re-states the
 * current draft alongside the question — otherwise every reply would rebuild
 * the draft from the last message alone and quietly drop what was already
 * agreed.
 *
 * Both directions parse defensively. A CLI-backed model can emit slightly
 * malformed JSON, and a block that fails to parse must degrade to "no preview
 * update" rather than breaking the conversation.
 */

const DRAFT_INPUT_PREFIX = "MULTICA_ISSUE_DRAFT_INPUT\n";

const COMPLETE_BLOCK = /\s*<issue_draft>[\s\S]*?<\/issue_draft>/g;
/** An unterminated block: the reply is still streaming, or the model never closed it. */
const OPEN_BLOCK = /\s*<issue_draft>[\s\S]*$/;

/**
 * The guided policy's question, as its own block. Separate from the draft block
 * because the two mean different things: the draft is a partial update to a
 * structured object, while a question is a turn-level affordance that has to
 * disappear once it is answered. An unterminated question block is stripped
 * like an unterminated draft block — a streaming reply must not leak markup
 * into the transcript.
 */
const COMPLETE_QUESTION_BLOCK =
  /\s*<issue_draft_question>[\s\S]*?<\/issue_draft_question>/g;
const OPEN_QUESTION_BLOCK = /\s*<issue_draft_question>[\s\S]*$/;

/** At most this many answers are offered per question. */
const MAX_QUESTION_OPTIONS = 6;

/**
 * Fields the carrier may revise. Every one is optional: the block is a partial
 * update, and an omitted or empty field means "no opinion", never "clear it".
 * That distinction is what stops a model that only restates the title from
 * wiping the description the user already approved.
 */
export type IssueDraftPatch = Partial<IssueDraftPayload>;

/**
 * The last `<issue_draft>` block in a reply, parsed. The last one wins because
 * a model that thinks out loud may draft twice; the instructions ask for the
 * final state.
 */
export function parseIssueDraftBlock(content: string): IssueDraftPatch | null {
  const matches = [...content.matchAll(new RegExp(COMPLETE_BLOCK))];
  const raw = matches[matches.length - 1]?.[0];
  if (!raw) return null;
  const inner = raw.replace(/^\s*<issue_draft>/, "").replace(/<\/issue_draft>\s*$/, "");
  const parsed = parseJsonObject(inner);
  if (!parsed) return null;
  const patch: IssueDraftPatch = {};
  for (const field of ["title", "description", "status", "priority"] as const) {
    const value = parsed[field];
    if (typeof value === "string" && value.trim().length > 0) {
      patch[field] = value;
    }
  }
  return patch;
}

/** The reply with every machine-readable block removed — what the conversation shows. */
export function stripIssueDraftDirectives(content: string): string {
  return content
    .replace(new RegExp(COMPLETE_BLOCK), "")
    .replace(OPEN_BLOCK, "")
    .replace(new RegExp(COMPLETE_QUESTION_BLOCK), "")
    .replace(OPEN_QUESTION_BLOCK, "")
    .trim();
}

/** One answer the guided policy proposes for its question. */
export interface IssueDraftQuestionOption {
  /** Short text on the answer chip. */
  label: string;
  /** What sending this answer actually says, phrased as the user would. */
  value: string;
  /** The option the carrier would pick itself. At most one per question. */
  recommended: boolean;
}

/** The single question an alignment turn is waiting on. */
export interface IssueDraftQuestion {
  question: string;
  options: IssueDraftQuestionOption[];
}

/**
 * The question block of a reply, parsed. Same rules as the draft block: the
 * last complete block wins, and anything malformed means "no question" rather
 * than a broken turn.
 *
 * Options are validated individually instead of all-or-nothing: a model that
 * emits one malformed option alongside three usable ones has still asked a
 * usable question, and the composer is always there for a free-text answer.
 */
export function parseIssueDraftQuestion(
  content: string,
): IssueDraftQuestion | null {
  const matches = [...content.matchAll(new RegExp(COMPLETE_QUESTION_BLOCK))];
  const raw = matches[matches.length - 1]?.[0];
  if (!raw) return null;
  const inner = raw
    .replace(/^\s*<issue_draft_question>/, "")
    .replace(/<\/issue_draft_question>\s*$/, "");
  const parsed = parseJsonObject(inner);
  if (!parsed) return null;

  const question = parsed.question;
  if (typeof question !== "string" || question.trim().length === 0) return null;

  const options: IssueDraftQuestionOption[] = [];
  const rawOptions = parsed.options;
  if (Array.isArray(rawOptions)) {
    for (const entry of rawOptions) {
      if (options.length >= MAX_QUESTION_OPTIONS) break;
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
      const option = entry as Record<string, unknown>;
      const label = option.label;
      const value = option.value;
      if (typeof label !== "string" || label.trim().length === 0) continue;
      if (typeof value !== "string" || value.trim().length === 0) continue;
      options.push({
        label: label.trim(),
        value: value.trim(),
        recommended: option.recommended === true,
      });
    }
  }
  return { question: question.trim(), options };
}

/**
 * The question the alignment is currently waiting on: the LAST message in the
 * transcript, when the carrier wrote it and it asks something.
 *
 * "Last message" is the whole rule, and it is why an answered question
 * disappears on its own — the user's next turn becomes the last message, so the
 * chips are gone before the carrier has replied, and a reply that asks nothing
 * clears them too. Only the last block of that message counts: a model that
 * thinks out loud may emit two.
 */
export function issueDraftPendingQuestion(
  messages: readonly ChatMessage[],
): { messageId: string; question: IssueDraftQuestion } | null {
  const last = messages[messages.length - 1];
  if (!last || last.role !== "assistant") return null;
  const question = parseIssueDraftQuestion(last.content);
  if (!question) return null;
  return { messageId: last.id, question };
}

/** One turn: what the user asked, plus the draft they are asking about. */
export function encodeIssueDraftInput(
  request: string,
  draft: IssueDraftPayload,
): string {
  return (
    DRAFT_INPUT_PREFIX +
    JSON.stringify({
      user_request: request,
      current_draft: {
        title: draft.title,
        description: draft.description,
        status: draft.status,
        priority: draft.priority,
      },
    })
  );
}

/**
 * Recovers the user's own words from a stored turn. Anything that is not an
 * envelope is returned unchanged, so a message written by an older build — or
 * by a person — still renders as itself.
 */
export function decodeIssueDraftInput(content: string): string {
  if (!content.startsWith(DRAFT_INPUT_PREFIX)) return content;
  const parsed = parseJsonObject(content.slice(DRAFT_INPUT_PREFIX.length));
  const request = parsed?.user_request;
  return typeof request === "string" ? request : content;
}

/**
 * Folds a parsed block into the draft the user is looking at. Patch fields
 * overwrite; everything else is preserved, including the fields the carrier is
 * not told about (assignee, project, parent) which the preview panel owns.
 */
export function mergeIssueDraftPayload(
  current: IssueDraftPayload,
  patch: IssueDraftPatch | null,
): IssueDraftPayload {
  if (!patch) return current;
  return {
    ...current,
    ...(patch.title !== undefined ? { title: patch.title } : {}),
    ...(patch.description !== undefined
      ? { description: patch.description }
      : {}),
    ...(patch.status !== undefined ? { status: patch.status } : {}),
    ...(patch.priority !== undefined ? { priority: patch.priority } : {}),
  };
}

/**
 * Whether the draft is worth creating an issue from. Only the title is
 * required: the server reads the rest out of the same object, and an empty
 * description is a legitimate issue. This is the client's own gate, so the
 * confirm button is never offered for something the server will refuse.
 */
export function issueDraftIsCreatable(draft: IssueDraftPayload): boolean {
  return draft.title.trim().length > 0;
}

function parseJsonObject(value: string): Record<string, unknown> | null {
  const attempts = [value];
  // Some CLI-backed models emit literal newlines inside a JSON string even
  // when told not to. Repair only JSON control characters inside strings —
  // object structure and every other syntax error still has to fail.
  attempts.push(escapeJsonStringControlCharacters(value));
  for (const attempt of attempts) {
    try {
      const parsed: unknown = JSON.parse(attempt);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // Try the repaired form, then give up: an unparseable block means no
      // preview update, not a broken conversation.
    }
  }
  return null;
}

/**
 * Escapes raw control characters that appear *inside* a JSON string literal.
 * Structure outside strings is left untouched, so a genuinely malformed object
 * still fails to parse.
 */
function escapeJsonStringControlCharacters(value: string): string {
  let out = "";
  let inString = false;
  let escaped = false;
  for (const char of value) {
    if (escaped) {
      out += char;
      escaped = false;
      continue;
    }
    if (char === "\\") {
      out += char;
      escaped = true;
      continue;
    }
    if (char === '"') {
      inString = !inString;
      out += char;
      continue;
    }
    if (inString && char === "\n") {
      out += "\\n";
      continue;
    }
    if (inString && char === "\r") {
      out += "\\r";
      continue;
    }
    if (inString && char === "\t") {
      out += "\\t";
      continue;
    }
    out += char;
  }
  return out;
}
