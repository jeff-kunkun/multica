import { QueryClient, type InfiniteData } from "@tanstack/react-query";
import { expect, it } from "vitest";
import { chatKeys } from "../chat/queries";
import type { ChatMessage, ChatMessagesPage } from "../types";
import { applyChatMessageToCache } from "./use-realtime-sync";

it("retains authenticated Bot provenance in the realtime message before any refetch", () => {
  const qc = new QueryClient();
  const session = "review-session";
  qc.setQueryData<ChatMessage[]>(chatKeys.messages(session), []);
  qc.setQueryData<InfiniteData<ChatMessagesPage>>(chatKeys.messagesPage(session), {
    pages: [{ messages: [], limit: 50, has_more: false, next_cursor: null }],
    pageParams: [null],
  });
  const event = {
    chat_session_id: session, message_id: "bot-message", role: "user" as const,
    content: "我拍板选 A", sender_user_id: "owner", task_id: "task",
    created_at: "2026-10-11T00:00:00Z",
    via_bot_id: "bot-pat-id", via_bot_name: "Grok",
  };
  applyChatMessageToCache(qc, event);
  const flat = qc.getQueryData<ChatMessage[]>(chatKeys.messages(session))![0];
  const paged = qc.getQueryData<InfiniteData<ChatMessagesPage>>(chatKeys.messagesPage(session))!.pages[0]!.messages[0];
  console.log("Bot event:", event.via_bot_id, event.via_bot_name);
  console.log("flat cache:", JSON.stringify(flat));
  console.log("paged cache:", JSON.stringify(paged));
  expect(flat).toMatchObject({ via_bot_id: event.via_bot_id, via_bot_name: event.via_bot_name });
  expect(paged).toMatchObject({ via_bot_id: event.via_bot_id, via_bot_name: event.via_bot_name });
  qc.clear();
});
