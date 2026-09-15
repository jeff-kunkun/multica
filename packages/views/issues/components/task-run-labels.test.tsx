import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";
import { useStatusLabel } from "./task-run-labels";

function wrapper(locale: "en" | "zh-Hans") {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <I18nProvider locale={locale} resources={RESOURCES}>
        {children}
      </I18nProvider>
    );
  };
}

describe("useStatusLabel", () => {
  it("localizes deferred instead of echoing the raw status", () => {
    const { result } = renderHook(() => useStatusLabel("deferred"), {
      wrapper: wrapper("zh-Hans"),
    });
    expect(result.current).toBe("重试中");
    expect(result.current).not.toBe("deferred");
  });

  it("uses the retrying label in English", () => {
    const { result } = renderHook(() => useStatusLabel("deferred"), {
      wrapper: wrapper("en"),
    });
    expect(result.current).toBe("Retrying");
  });
});
