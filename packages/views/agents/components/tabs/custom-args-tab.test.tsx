// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: vi.fn().mockResolvedValue(true),
}));

import { toast } from "sonner";
import { copyText } from "@multica/ui/lib/clipboard";
import { CustomArgsTab } from "./custom-args-tab";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: ["--profile", "research"],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const runtimeDevice = {
  launch_header: "codex app-server",
} as RuntimeDevice;

function renderTab(
  overrides: Partial<Agent> = {},
  onSave = vi.fn().mockResolvedValue(undefined),
  device: RuntimeDevice = runtimeDevice,
) {
  const result = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <CustomArgsTab
        agent={{ ...baseAgent, ...overrides }}
        runtimeDevice={device}
        onSave={onSave}
      />
    </I18nProvider>,
  );

  return { ...result, onSave };
}

const agyDevice = {
  ...runtimeDevice,
  provider: "antigravity",
  launch_header: "agy",
} as RuntimeDevice;

const agyDeviceWithHome = {
  ...agyDevice,
  metadata: { home_dir: "/Users/agy-host" },
} as RuntimeDevice;

function hideProcessHome() {
  vi.stubEnv("HOME", "");
  vi.stubEnv("USERPROFILE", "");
  delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
}

describe("CustomArgsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    delete (globalThis as { desktopAPI?: unknown }).desktopAPI;
  });

  it("renders configured arguments as a list, not persistent inputs", () => {
    renderTab();

    expect(screen.getByText("--profile")).toBeInTheDocument();
    expect(screen.getByText("research")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText("codex app-server --profile research")).toBeInTheDocument();
  });

  it("uses one editor to add one argument token", async () => {
    const user = userEvent.setup();
    renderTab({ custom_args: [] });

    await user.click(screen.getByRole("button", { name: /add argument/i }));
    const input = screen.getByRole("textbox", { name: /new argument/i });
    await user.type(input, "value with spaces");
    await user.click(screen.getByRole("button", { name: /^add$/i }));

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText("value with spaces")).toBeInTheDocument();
  });

  it("edits a list item in place with the same single editor", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(screen.getByRole("button", { name: /edit argument 1/i }));
    const input = screen.getByRole("textbox", { name: /argument 1/i });
    await user.clear(input);
    await user.type(input, "--model");
    await user.click(screen.getByRole("button", { name: /update/i }));

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText("--model")).toBeInTheDocument();
    expect(screen.queryByText("--profile")).not.toBeInTheDocument();
  });

  it("preserves spaces inside one token when saving", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab({ custom_args: [] });

    await user.click(screen.getByRole("button", { name: /add argument/i }));
    await user.type(
      screen.getByRole("textbox", { name: /new argument/i }),
      "value with spaces",
    );
    await user.click(screen.getByRole("button", { name: /^add$/i }));
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(onSave).toHaveBeenCalledWith({ custom_args: ["value with spaces"] });
  });

  it("does not show AGY account slots for other runtimes", () => {
    renderTab();

    expect(screen.queryByRole("radiogroup", { name: /agy account/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: /account 2/i })).not.toBeInTheDocument();
  });

  it("shows account 1, 2, and 3 radios for AGY", () => {
    renderTab({ custom_args: [] }, undefined, agyDeviceWithHome);

    expect(screen.getByRole("radio", { name: /account 1/i })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /account 2/i })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /account 3/i })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /custom/i })).toBeInTheDocument();
  });

  it("fills account 2 from runtime metadata.home_dir without HOME or desktopAPI", async () => {
    hideProcessHome();
    const user = userEvent.setup();
    const { onSave } = renderTab({ custom_args: [] }, undefined, agyDeviceWithHome);

    expect(screen.getByRole("radio", { name: /account 1/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await user.click(screen.getByRole("radio", { name: /account 2/i }));

    expect(screen.getByRole("radio", { name: /account 2/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(
      screen.getByText("agy --gemini_dir=/Users/agy-host/.gemini-account2"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("agy --gemini_dir /Users/agy-host/.gemini-account2"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/macOS Keychain stores one Google login/i),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^save$/i }));
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", "/Users/agy-host/.gemini-account2"],
    });
  });

  it("fills an absolute Account 2 path when the current Gemini dir is ~/.gemini", async () => {
    vi.stubEnv("HOME", "/Users/you");
    vi.stubEnv("USERPROFILE", "");
    const user = userEvent.setup();
    const { onSave } = renderTab(
      { custom_args: ["--gemini_dir", "~/.gemini"] },
      undefined,
      agyDevice,
    );

    await user.click(screen.getByRole("radio", { name: /account 2/i }));
    expect(
      screen.getByText("agy --gemini_dir=/Users/you/.gemini-account2"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^save$/i }));
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", "/Users/you/.gemini-account2"],
    });
  });

  it("fills an absolute Account 2 path when the current Gemini dir is ~\\.gemini", async () => {
    const user = userEvent.setup();
    vi.stubEnv("HOME", "");
    vi.stubEnv("USERPROFILE", "C:\\Users\\you");
    const { onSave } = renderTab(
      { custom_args: ["--gemini_dir", "~\\.gemini"] },
      undefined,
      agyDevice,
    );

    await user.click(screen.getByRole("radio", { name: /account 2/i }));
    expect(
      screen.getByText("agy --gemini_dir=C:\\Users\\you\\.gemini-account2"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^save$/i }));
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", "C:\\Users\\you\\.gemini-account2"],
    });
  });

  it("does not save a tilde path when account 2 is clicked without any home source", async () => {
    hideProcessHome();
    const user = userEvent.setup();
    const { onSave } = renderTab({ custom_args: [] }, undefined, agyDevice);

    await user.click(screen.getByRole("radio", { name: /account 2/i }));

    expect(toast.error).toHaveBeenCalled();
    expect(screen.getByRole("radio", { name: /account 1/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(
      screen.queryByText("agy --gemini_dir=~/.gemini-account2"),
    ).not.toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("uses the desktop home directory when renderer env is unavailable", async () => {
    hideProcessHome();
    const user = userEvent.setup();
    Object.defineProperty(globalThis, "desktopAPI", {
      configurable: true,
      value: { homeDir: "/Users/desktop" },
    });
    const { onSave } = renderTab({ custom_args: [] }, undefined, agyDevice);

    await user.click(screen.getByRole("radio", { name: /account 2/i }));
    expect(
      screen.getByText("agy --gemini_dir=/Users/desktop/.gemini-account2"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", "/Users/desktop/.gemini-account2"],
    });
  });

  it("copies the AGY sign-in command for the selected slot", async () => {
    const user = userEvent.setup();
    renderTab(
      { custom_args: ["--gemini_dir", "/Users/you/.gemini"] },
      undefined,
      agyDevice,
    );

    await user.click(screen.getByRole("radio", { name: /account 2/i }));
    await user.click(screen.getByRole("button", { name: /copy sign-in command/i }));

    expect(copyText).toHaveBeenCalledWith(
      "agy --gemini_dir=/Users/you/.gemini-account2",
    );
  });

  it("fills account 3 from runtime metadata.home_dir and saves the isolated path", async () => {
    hideProcessHome();
    const user = userEvent.setup();
    const { onSave } = renderTab({ custom_args: [] }, undefined, agyDeviceWithHome);

    await user.click(screen.getByRole("radio", { name: /account 3/i }));

    expect(screen.getByRole("radio", { name: /account 3/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(
      screen.getByText("agy --gemini_dir=/Users/agy-host/.gemini-account3"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("agy --gemini_dir /Users/agy-host/.gemini-account3"),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^save$/i }));
    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--gemini_dir", "/Users/agy-host/.gemini-account3"],
    });
  });

  it("does not save a tilde path when account 3 is clicked without any home source", async () => {
    hideProcessHome();
    const user = userEvent.setup();
    const { onSave } = renderTab({ custom_args: [] }, undefined, agyDevice);

    await user.click(screen.getByRole("radio", { name: /account 3/i }));

    expect(toast.error).toHaveBeenCalled();
    expect(screen.getByRole("radio", { name: /account 1/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(
      screen.queryByText("agy --gemini_dir=~/.gemini-account3"),
    ).not.toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("copies the AGY sign-in command for account 3", async () => {
    const user = userEvent.setup();
    renderTab(
      { custom_args: ["--gemini_dir", "/Users/you/.gemini"] },
      undefined,
      agyDevice,
    );

    await user.click(screen.getByRole("radio", { name: /account 3/i }));
    await user.click(screen.getByRole("button", { name: /copy sign-in command/i }));

    expect(copyText).toHaveBeenCalledWith(
      "agy --gemini_dir=/Users/you/.gemini-account3",
    );
  });

  it("lets custom slot edit the Gemini directory and keeps other args", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab(
      { custom_args: ["--profile", "research", "--gemini_dir", "/Users/you/.gemini"] },
      undefined,
      agyDevice,
    );

    await user.click(screen.getByRole("radio", { name: /custom/i }));
    const input = screen.getByRole("textbox", { name: /gemini directory/i });
    await user.clear(input);
    await user.type(input, "/Users/you/.gemini-work");
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(onSave).toHaveBeenCalledWith({
      custom_args: ["--profile", "research", "--gemini_dir", "/Users/you/.gemini-work"],
    });
  });
});
