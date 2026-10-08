import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { IssueSourceChatLink } from "./issue-source-chat-link";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: { id: "chat-1", title: "Multica · 回执" } }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ chatSession: (id: string) => `/acme/chat/${id}` }),
}));

vi.mock("../../i18n", () => ({
  useT: () => ({ t: (pick: (keys: Record<string, Record<string, string>>) => string) => pick({ detail: new Proxy({}, { get: (_, k) => String(k) }) }) }),
}));

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues/issue-1",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => `https://app.example${path}`,
};

describe("IssueSourceChatLink", () => {
  it("links the issue back to the chat it was dispatched from", () => {
    render(
      <NavigationProvider value={navigation}>
        <IssueSourceChatLink sessionId="chat-1" />
      </NavigationProvider>,
    );
    const link = screen.getByRole("link", { name: /source_chat/ });
    expect(link).toHaveAttribute("href", "/acme/chat/chat-1");
  });
});
