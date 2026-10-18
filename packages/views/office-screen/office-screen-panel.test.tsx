import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import en from "../locales/en/office.json";
import zh from "../locales/zh-Hans/office.json";
import { resetOfficeScreenStore, type OfficeScreenFrame, type OfficeScreenState } from "../platform/office-screen";
import { OfficeScreenPanel } from "./office-screen-panel";

const running: OfficeScreenState = {
  supported: true,
  latch: { state: "running", generation: 2, changedAt: null },
  receipt: null,
  view: { enabled: true, status: "streaming", reason: "ok", windows: 1, lastFrameAt: null },
  permission: "granted",
};

function stoppedState(over: Partial<NonNullable<OfficeScreenState["receipt"]>> = {}): OfficeScreenState {
  return {
    ...running,
    latch: { state: "stopped", generation: 3, changedAt: null },
    receipt: { generation: 3, requested: "done", inputSealed: "done", tasksStopped: "pending", activeTasks: 2, ...over },
  };
}

function installAPI(initial: OfficeScreenState) {
  let onState: ((s: OfficeScreenState) => void) | null = null;
  let onFrame: ((f: OfficeScreenFrame) => void) | null = null;
  const api = {
    subscribe: vi.fn(async () => initial),
    unsubscribe: vi.fn(async () => {}),
    stop: vi.fn(async () => stoppedState()),
    resume: vi.fn(async () => ({ ok: true as const, state: running })),
    setViewEnabled: vi.fn(async (enabled: boolean) => ({ ...initial, view: { ...initial.view, enabled } })),
    requestPermission: vi.fn(async () => initial),
    openPermissionSettings: vi.fn(async () => {}),
    pagePreviews: vi.fn(async () => []),
    onState: (cb: (s: OfficeScreenState) => void) => {
      onState = cb;
      return () => (onState = null);
    },
    onFrame: (cb: (f: OfficeScreenFrame) => void) => {
      onFrame = cb;
      return () => (onFrame = null);
    },
  };
  (window as unknown as { desktopAPI: unknown }).desktopAPI = { officeScreen: api };
  return {
    api,
    pushState: (s: OfficeScreenState) => act(() => onState?.(s)),
    pushFrame: (f: OfficeScreenFrame) => act(() => onFrame?.(f)),
  };
}

afterEach(() => {
  delete (window as unknown as { desktopAPI?: unknown }).desktopAPI;
  resetOfficeScreenStore();
});

describe("OfficeScreenPanel", () => {
  it("renders nothing on web", () => {
    const { container } = renderWithI18n(<OfficeScreenPanel />);
    expect(container).toBeEmptyDOMElement();
  });

  it("stops with one click and shows each process's confirmation", async () => {
    const { api } = installAPI(running);
    renderWithI18n(<OfficeScreenPanel />);
    const stop = await screen.findByRole("button", { name: en.screen.stop });
    fireEvent.click(stop);
    await waitFor(() => expect(api.stop).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(en.screen.stopped)).toBeInTheDocument();
    expect(screen.getByText(en.screen.step_input)).toBeInTheDocument();
    expect(screen.getByText("2 still exiting…")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.screen.stop })).not.toBeInTheDocument();
  });

  it("says plainly when a process is too old for the stop to reach it", async () => {
    installAPI(stoppedState({ inputSealed: "outdated", tasksStopped: "done", activeTasks: 0 }));
    renderWithI18n(<OfficeScreenPanel />);
    expect(await screen.findByText(en.screen.step_outdated)).toBeInTheDocument();
  });

  it("resumes against the generation it saw and reports a changed latch", async () => {
    const { api } = installAPI(stoppedState());
    api.resume.mockResolvedValueOnce({ ok: false, error: "generation_mismatch", state: stoppedState() } as never);
    renderWithI18n(<OfficeScreenPanel />);
    fireEvent.click(await screen.findByRole("button", { name: en.screen.resume }));
    await waitFor(() => expect(api.resume).toHaveBeenCalledWith(3));
    expect(await screen.findByText(en.screen.resume_changed)).toBeInTheDocument();
  });

  it("shows live frames and can hide the picture on its own", async () => {
    const { api, pushFrame } = installAPI(running);
    renderWithI18n(<OfficeScreenPanel />);
    await screen.findByText(en.screen.running);
    pushFrame({ seq: 1, width: 1280, height: 720, src: "data:image/jpeg;base64,AAAA", at: Date.now() });
    expect(await screen.findByRole("img", { name: en.screen.title })).toHaveAttribute("src", "data:image/jpeg;base64,AAAA");
    expect(screen.getByText(en.screen.view_live)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: en.screen.view_off }));
    await waitFor(() => expect(api.setViewEnabled).toHaveBeenCalledWith(false));
    expect(await screen.findByText(en.screen.view_hidden)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.screen.stop })).toBeInTheDocument();
  });

  it("pauses the picture with a reason instead of showing an unknown window", async () => {
    installAPI({ ...running, view: { ...running.view, status: "blocked", reason: "unowned_window" } });
    renderWithI18n(<OfficeScreenPanel />);
    expect(await screen.findByText(en.screen.view_unowned_window)).toBeInTheDocument();
  });

  it("asks for screen recording only when clicked, in Chinese too", async () => {
    const { api } = installAPI({ ...running, permission: "missing", view: { ...running.view, status: "blocked", reason: "permission" } });
    renderWithI18n(<OfficeScreenPanel />, { locale: "zh-Hans" });
    expect(await screen.findByText(zh.screen.view_permission.replace("{{app}}", zh.screen.app_name))).toBeInTheDocument();
    expect(api.requestPermission).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: zh.screen.view_permission_allow }));
    await waitFor(() => expect(api.requestPermission).toHaveBeenCalledTimes(1));
  });

  it("bar keeps the stop button visible while collapsed and shows the receipt when stopped", async () => {
    const { pushState } = installAPI(running);
    renderWithI18n(<OfficeScreenPanel variant="bar" />);
    expect(await screen.findByRole("button", { name: en.screen.stop })).toBeInTheDocument();
    expect(screen.queryByText(en.screen.read_only_note)).not.toBeInTheDocument();
    pushState(stoppedState());
    expect(await screen.findByText(en.screen.step_tasks)).toBeInTheDocument();
  });
});
