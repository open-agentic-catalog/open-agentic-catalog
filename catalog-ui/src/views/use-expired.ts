import { useEffect, useState } from "react";

/** Local guidance only. No network request, host-clock change or server authority. */
export function useExpired(expiresAt: string | undefined): boolean {
  const [, tick] = useState(0);
  useEffect(() => {
    if (!expiresAt || Date.parse(expiresAt) <= Date.now()) return;
    const timer = window.setInterval(() => {
      tick((value) => value + 1);
      if (Date.parse(expiresAt) <= Date.now()) window.clearInterval(timer);
    }, 1000);
    return () => window.clearInterval(timer);
  }, [expiresAt]);
  return expiresAt !== undefined && !(Date.parse(expiresAt) > Date.now());
}
