import { controlIcons } from "./Icon";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { decodeRuntime, type Runtime } from "../api/session";
import { App } from "./App";
import { ErrorBoundary } from "./ErrorBoundary";
const PublicDiscovery = lazy(() => import("../views/PublicDiscovery"));

/** No authenticated client exists until the host settings have been validated. */
export function Bootstrap({ url }: { url: URL }) {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<Runtime | "loading" | "failed">("loading");
  const recovery = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    let current = true;
    const controller = new AbortController();
    void fetch(url, {
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      signal: controller.signal,
    })
      .then(async (response) => {
        if (response.status !== 200)
          throw new Error("Configuration unavailable");
        const value: unknown = await response.json();
        const runtime = decodeRuntime(value);
        if (current) setState(runtime);
      })
      .catch(() => {
        if (current) setState("failed");
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [url, attempt]);
  useEffect(() => {
    if (state === "failed" || (attempt > 0 && state === "loading"))
      recovery.current?.focus();
  }, [state, attempt]);
  return typeof state === "object" ? (
    <ErrorBoundary>
      {location.pathname === `${state.basePath}/discover` ? (
        state.publicDiscoveryEnabled ? (
          <Suspense
            fallback={
              <main id="main">
                <p role="status">Loading public discovery...</p>
              </main>
            }
          >
            <PublicDiscovery runtime={state} />
          </Suspense>
        ) : (
          <main id="main">
            <h1>Public discovery is not enabled</h1>
          </main>
        )
      ) : (
        <App runtime={state} />
      )}
    </ErrorBoundary>
  ) : (
    <main id="main" className="bootstrap">
      <h1 ref={recovery} tabIndex={-1}>
        {state === "loading" ? "Starting Catalog" : "Catalog unavailable"}
      </h1>
      {state === "loading" ? (
        <p role="status">Loading UI settings...</p>
      ) : (
        <>
          <p role="alert">
            The UI settings could not be loaded. Reload to try again.
          </p>
          <button
            onClick={() => {
              setState("loading");
              setAttempt((value) => value + 1);
            }}
          >
            {controlIcons["refresh"]} Reload
          </button>
        </>
      )}
    </main>
  );
}
