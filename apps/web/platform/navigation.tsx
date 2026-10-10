"use client";

import { Suspense, useEffect, useSyncExternalStore } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "@multica/views/navigation";
import { isPhoneViewport, isReservedSlug, paths } from "@multica/core/paths";
import {
  canGoBackInApp,
  goHome,
  installInAppHistoryDepth,
  seedHomeBelow,
} from "./in-app-history";

/**
 * Web half of the `multica:navigate` bridge — the event shared content
 * (comments, chat, issue descriptions) fires when a link resolves to an in-app
 * destination. A plain click ("push") is a router push in place. A modifier
 * click normally never reaches here on web — real anchors leave it to the
 * browser — but the editor must intercept every click (contenteditable
 * anchors don't navigate natively), and for those `window.open` is the
 * closest the web can get: JS cannot open a background tab, so both tab
 * dispositions land as a foreground browser tab.
 */
function useInternalLinkHandler(router: ReturnType<typeof useRouter>) {
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (
        e as CustomEvent<{ path?: string; disposition?: string }>
      ).detail;
      const path = detail?.path;
      if (!path) return;
      if (
        detail?.disposition === "background-tab" ||
        detail?.disposition === "foreground-tab"
      ) {
        window.open(
          window.location.origin + path,
          "_blank",
          "noopener,noreferrer",
        );
        return;
      }
      router.push(path);
    };
    window.addEventListener("multica:navigate", handler);
    return () => window.removeEventListener("multica:navigate", handler);
  }, [router]);
}

/**
 * The fragment is client-only state Next.js never surfaces: `usePathname()`
 * drops it, and a `router.replace("/x#y")` mutates `window.location` without
 * a render of its own. Reading it through an external store re-reads the URL
 * on every render and re-renders on the events that change it behind React's
 * back, so `adapter.hash` is never a stale copy.
 */
function subscribeToHash(onStoreChange: () => void): () => void {
  window.addEventListener("hashchange", onStoreChange);
  window.addEventListener("popstate", onStoreChange);
  return () => {
    window.removeEventListener("hashchange", onStoreChange);
    window.removeEventListener("popstate", onStoreChange);
  };
}

function NavigationProviderInner({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const hash = useSyncExternalStore(
    subscribeToHash,
    () => window.location.hash,
    () => "",
  );
  useInternalLinkHandler(router);
  useEffect(installInAppHistoryDepth, []);

  // A phone opens on Chat. The home-screen icon launches at /inbox (manifest
  // start_url), which would leave Inbox under every Back; a fresh launch that
  // lands on a bare inbox is moved onto Chat instead. Any other page opened
  // cold (a task link, a notification) gets Chat slipped in underneath, so Back
  // from it lands on Chat rather than leaving the app.
  useEffect(() => {
    if (!isPhoneViewport() || canGoBackInApp()) return;
    const segments = window.location.pathname.split("/").filter(Boolean);
    const slug = segments[0];
    if (!slug || isReservedSlug(slug) || segments.length < 2) return;
    const chat = paths.workspace(slug).chat();
    if (segments[1] === "chat") return;
    if (segments.length === 2 && segments[1] === "inbox" && !window.location.search) {
      router.replace(chat);
      return;
    }
    seedHomeBelow(chat);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the launch only
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") return;
    if (typeof window.matchMedia !== "function") return;
    const media = window.matchMedia("(pointer: coarse) and (max-width: 767px)");
    if (!media.matches) return;
    let startX = 0;
    let startY = 0;
    let tracking = false;
    const onTouchStart = (event: TouchEvent) => {
      const touch = event.touches[0];
      if (!touch) return;
      tracking = touch.clientX <= 24 && canGoBackInApp();
      startX = touch.clientX;
      startY = touch.clientY;
    };
    const onTouchMove = (event: TouchEvent) => {
      if (!tracking) return;
      const touch = event.touches[0];
      if (!touch) return;
      if (touch.clientX - startX < -8 || Math.abs(touch.clientY - startY) > 48) tracking = false;
    };
    const onTouchEnd = (event: TouchEvent) => {
      if (!tracking) return;
      const touch = event.changedTouches[0];
      tracking = false;
      if (!touch) return;
      if (touch.clientX - startX >= 80 && Math.abs(touch.clientY - startY) < 72) router.back();
    };
    window.addEventListener("touchstart", onTouchStart, { passive: true });
    window.addEventListener("touchmove", onTouchMove, { passive: true });
    window.addEventListener("touchend", onTouchEnd, { passive: true });
    return () => {
      window.removeEventListener("touchstart", onTouchStart);
      window.removeEventListener("touchmove", onTouchMove);
      window.removeEventListener("touchend", onTouchEnd);
    };
  }, [router]);

  const adapter: NavigationAdapter = {
    push: router.push,
    replace: router.replace,
    back: router.back,
    forward: router.forward,
    canGoBack: canGoBackInApp,
    switchTab: (path: string, home: string, roots: string[]) => {
      if (!isPhoneViewport()) {
        router.push(path);
        return;
      }
      const here = pathname.replace(/\/+$/, "");
      if (here === path) return;
      const onRoot = roots.includes(here) && here !== home;
      if (path === home && goHome(home)) return;
      if (onRoot) router.replace(path);
      else router.push(path);
    },
    pathname,
    searchParams: new URLSearchParams(searchParams.toString()),
    hash,
    getShareableUrl: (path: string) =>
      typeof window === "undefined" ? path : window.location.origin + path,
    // router.prefetch is a no-op in dev mode by Next.js design; in production
    // it warms the RSC payload + route chunk so the next push() commits with
    // no network round-trip. Safe to call repeatedly — Next dedupes internally.
    prefetch: (path: string) => {
      router.prefetch(path);
    },
  };

  return <NavigationProvider value={adapter}>{children}</NavigationProvider>;
}

export function WebNavigationProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <Suspense>
      <NavigationProviderInner>{children}</NavigationProviderInner>
    </Suspense>
  );
}
