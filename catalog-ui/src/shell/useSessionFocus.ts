import { useEffect, useLayoutEffect, useRef, type RefObject } from "react";

/** A lazy page may arrive after the session changes. Never reclaim user-moved focus. */
export function useSessionFocus(
  hasSession: boolean,
  loading: boolean,
  busy: boolean,
  explicit: RefObject<boolean>,
) {
  const lastFocus = useRef<HTMLElement | null>(null);
  const hadSession = useRef(false);
  useEffect(() => {
    const focused = (event: FocusEvent) => {
      if (event.target instanceof HTMLElement && event.target !== document.body)
        lastFocus.current = event.target;
    };
    document.addEventListener("focusin", focused);
    return () => document.removeEventListener("focusin", focused);
  }, []);
  useLayoutEffect(() => {
    const removed =
      hadSession.current &&
      !hasSession &&
      lastFocus.current &&
      !lastFocus.current.isConnected;
    hadSession.current = hasSession;
    if (!removed && (loading || busy || !explicit.current)) return;
    explicit.current = false;
    // Every shell state owns one main landmark, including lazy loading/recovery.
    const main = document.getElementById("main")!;
    const focusHeading = () => {
      const heading = main.querySelector<HTMLElement>("h1");
      if (!heading) return false;
      heading.tabIndex = -1;
      heading.focus();
      return true;
    };
    if (focusHeading()) return;
    main.tabIndex = -1;
    main.focus();
    const observer = new MutationObserver(() => {
      if (document.activeElement !== main || focusHeading())
        observer.disconnect();
    });
    observer.observe(main, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [hasSession, loading, busy, explicit]);
}
