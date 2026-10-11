import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ViaBot } from "./via-bot";

vi.mock("../i18n", () => ({
  useT: () => ({ t: (_key: unknown, values: { name: string }) => `经 ${values.name}` }),
}));

describe("ViaBot", () => {
  it("shows the preserved bot name", () => {
    render(<ViaBot name="Stopped Bot" />);
    expect(screen.getByText("经 Stopped Bot")).toBeTruthy();
  });
  it("adds no attribution for direct member messages", () => {
    const { container } = render(<ViaBot />);
    expect(container.textContent).toBe("");
  });
});
