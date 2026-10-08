/**
 * Where a chat's tickets render (DENE-1665).
 *
 * Mirrors `groupChatTickets` in packages/views/chat/components/chat-ticket-card.tsx:
 * each ticket hangs under the reply of the turn that opened it — the first
 * assistant message created at or after the ticket. A ticket opened by a turn
 * still running has no such reply yet and goes to `tail`, the list's last row.
 */
import type { ChatTicket } from "@multica/core/types";

export function groupChatTickets(
  messages: { id: string; role: string; created_at: string }[],
  tickets: ChatTicket[],
): { byMessage: Map<string, ChatTicket[]>; tail: ChatTicket[] } {
  const replies = messages
    .filter((m) => m.role === "assistant")
    .map((m) => ({ id: m.id, at: Date.parse(m.created_at) }))
    .sort((a, b) => a.at - b.at);
  const byMessage = new Map<string, ChatTicket[]>();
  const tail: ChatTicket[] = [];
  for (const ticket of tickets) {
    const at = Date.parse(ticket.created_at);
    const reply = replies.find((r) => r.at >= at);
    if (!reply) {
      tail.push(ticket);
      continue;
    }
    const group = byMessage.get(reply.id);
    if (group) group.push(ticket);
    else byMessage.set(reply.id, [ticket]);
  }
  return { byMessage, tail };
}
