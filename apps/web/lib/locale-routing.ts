import {
  matchLocale,
  SUPPORTED_LOCALES,
  type SupportedLocale,
} from "@multica/core/i18n";

export const MULTICA_LOCALE_HEADER = "x-multica-locale";

export function isSupportedLocale(
  value: string | null,
): value is SupportedLocale {
  return (
    value !== null &&
    (SUPPORTED_LOCALES as readonly string[]).includes(value)
  );
}

export function resolveLocaleFromSignals({
  cookieLocale,
}: {
  cookieLocale?: string | null;
  acceptLanguage?: string | null;
}): SupportedLocale {
  // Browser languages (acceptLanguage) are ignored on purpose: the office
  // defaults to Chinese and only an explicit choice (the cookie) switches.
  return matchLocale(cookieLocale ? [cookieLocale] : []);
}
