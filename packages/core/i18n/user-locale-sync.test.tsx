// @vitest-environment jsdom
import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleAdapterProvider } from "./adapter-context";
import { I18nProvider } from "./provider";
import { UserLocaleSync } from "./user-locale-sync";
import type { SupportedLocale } from "./types";

vi.mock("../auth", () => ({
  useAuthStore: (selector: (state: { user: { language: string } }) => unknown) =>
    selector({ user: { language: "zh-Hans" } }),
}));

afterEach(cleanup);

it("restores a saved language on another device and stops reloading once it matches", () => {
  const reload = vi.fn();
  const location = window.location;
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { reload },
  });
  const adapter = {
    getUserChoice: () => null,
    getSystemPreferences: () => ["en"],
    persist: vi.fn(),
  };
  const resources = {
    en: { common: { save: "Save" } },
    "zh-Hans": { common: { save: "保存" } },
  };
  const tree = (locale: SupportedLocale) => (
    <I18nProvider locale={locale} resources={resources}>
      <LocaleAdapterProvider adapter={adapter}>
        <UserLocaleSync />
      </LocaleAdapterProvider>
    </I18nProvider>
  );

  try {
    const first = render(tree("en"));
    expect(adapter.persist).toHaveBeenCalledExactlyOnceWith("zh-Hans");
    expect(reload).toHaveBeenCalledTimes(1);
    first.unmount();

    adapter.persist.mockClear();
    reload.mockClear();
    render(tree("zh-Hans"));
    expect(adapter.persist).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
  } finally {
    Object.defineProperty(window, "location", {
      configurable: true,
      value: location,
    });
  }
});
