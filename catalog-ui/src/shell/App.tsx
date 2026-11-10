import { PermissionDenied } from "../views/workspace";
import { SignInChoices } from "./SignInChoices";
import { availablePermission, permits } from "../api/permissions";
import { kindChoices } from "../resource-kinds";
// The shell owns identity lifetime, lazy routes and appearance across all workflow screens.
import {
  lazy,
  Suspense,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { APIError, canRead, canReadLocal, makeAPI } from "../api/client";
import {
  documentationLink,
  loginFailureFromSearch,
  type LoginOptions,
} from "../api/session";
import type { Runtime, Session } from "../api/client";
import { Icon, controlIcons } from "./Icon";
import { useSessionFocus } from "./useSessionFocus";
import { QuickNavigation } from "./QuickNavigation";
import {
  diagnosticError,
  type DiagnosticError,
} from "../views/diagnostic-details";
import { navigationGroups, pageContext } from "./navigation";
import "../views/workspace.css";
import { OperationProvider } from "../views/operation-context";
import { InvestigationProvider } from "../views/audit-handoff";
const MyWork = lazy(() =>
  import("../views/Personal").then((m) => ({ default: m.MyWork })),
);
const DiscoveryWorkspace = lazy(() => import("../views/DiscoveryWorkspace"));
const Access = lazy(() => import("../views/Access"));
const Namespaces = lazy(() => import("../views/Namespaces"));
const Publishers = lazy(() => import("../views/Publishers"));
const Jobs = lazy(() => import("../views/Jobs"));
const Overview = lazy(() => import("../views/Overview"));
const TrustAssessment = lazy(() => import("../views/TrustAssessment"));
const Service = lazy(() => import("../views/Service"));
const Resources = lazy(() => import("../views/Resources"));
const Versions = lazy(() => import("../views/Versions"));
const VersionRevision = lazy(() =>
  import("../views/Versions").then((m) => ({ default: m.VersionRevision })),
);
const Listings = lazy(() => import("../views/Listings"));
const ListingRevision = lazy(() =>
  import("../views/Listings").then((m) => ({ default: m.ListingRevision })),
);
const Reviews = lazy(() => import("../views/Reviews"));
const Release = lazy(() =>
  import("../views/Reviews").then((m) => ({ default: m.Release })),
);
const Authorization = lazy(() => import("../views/Authorization"));
const Settings = lazy(() => import("../views/Settings"));
const Members = lazy(() => import("../views/Members"));
const Publication = lazy(() => import("../views/Publication"));
const Audit = lazy(() => import("../views/Audit"));
const Ratings = lazy(() => import("../views/Ratings"));
const OwnRatings = lazy(() =>
  import("../views/Ratings").then((m) => ({ default: m.OwnRatings })),
);
const Feedback = lazy(() => import("../views/Feedback"));
const OwnFeedback = lazy(() =>
  import("../views/Feedback").then((m) => ({ default: m.OwnFeedback })),
);
const Webhooks = lazy(() => import("../views/Webhooks"));
const Federation = lazy(() => import("../views/Federation"));
const Inspection = lazy(() =>
  import("../views/Federation").then((m) => ({ default: m.Inspection })),
);
const SourceSnapshot = lazy(() =>
  import("../views/SourceSnapshots").then((m) => ({
    default: m.SourceSnapshot,
  })),
);
const Observations = lazy(() =>
  import("../views/Observations").then((m) => ({ default: m.Observations })),
);
const ObservationGroup = lazy(() =>
  import("../views/Observations").then((m) => ({
    default: m.ObservationGroup,
  })),
);
const FederationSearch = lazy(() =>
  import("../views/FederationSearch").then((m) => ({
    default: m.FederationSearch,
  })),
);

const authoringScreens = {
  resources: Resources,
  versions: Versions,
  "version-revisions": VersionRevision,
  listings: Listings,
  "listing-revisions": ListingRevision,
  reviews: Reviews,
  releases: Release,
};
const managementScreens = {
  overview: Overview,
  "trust-assessments": TrustAssessment,
  sources: Federation,
  inspections: Inspection,
  "source-snapshots": SourceSnapshot,
  observations: Observations,
  "observation-group": ObservationGroup,
  "federation-search": FederationSearch,
  members: Members,
  settings: Settings,
  "access-roles": Authorization,
  "access-groups": Authorization,
  "access-assignments": Authorization,
  publication: Publication,
  audit: Audit,
  webhooks: Webhooks,
  ratings: Ratings,
  "my-ratings": OwnRatings,
  feedback: Feedback,
  "my-feedback": OwnFeedback,
};

const Help = lazy(() => import("../views/Help"));
const APIReference = lazy(() => import("../views/APIReference"));

/** Own session changes, route selection and preferences. Screens receive the
 * current access context and report failures here; they never handle tokens. */
export function App({ runtime }: { runtime: Runtime }) {
  const client = useMemo(() => makeAPI(runtime), [runtime]);
  const [tabID] = useState(() => crypto.randomUUID());
  const refreshingAccess = useRef<AbortSignal | undefined>(undefined);
  const header = useRef<HTMLElement>(null);
  const workspace = useRef<HTMLDivElement>(null);
  const preferences = useRef<HTMLDetailsElement>(null);
  const account = useRef<HTMLDetailsElement>(null);
  const menuToggle = useRef<HTMLButtonElement>(null);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      for (const disclosure of [preferences.current, account.current]) {
        if (
          event.target instanceof Node &&
          disclosure &&
          !disclosure.contains(event.target)
        )
          disclosure.open = false;
      }
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, []);
  const [session, setSession] = useState<Session>();
  // A refreshed/replaced identity must never inherit an open private panel.
  useEffect(() => {
    if (account.current) account.current.open = false;
  }, [session]);
  const [loading, setLoading] = useState(true);
  // Wrapping and enlarged text change the sticky header's actual height.
  useLayoutEffect(() => {
    const element = workspace.current;
    if (!element) return;
    const topbar = header.current!;
    let active = true;
    const measure = () => {
      if (active) {
        element.style.setProperty(
          "--header-height",
          `${topbar.getBoundingClientRect().height}px`,
        );
        // Header wrapping can shrink the menu after native focus scrolling.
        element
          .querySelector<HTMLElement>("#workspace-navigation :focus")
          ?.scrollIntoView({ block: "nearest", inline: "nearest" });
      }
    };
    measure();
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(measure);
    observer?.observe(topbar);
    return () => {
      active = false;
      observer?.disconnect();
      element.style.removeProperty("--header-height");
    };
  }, [loading, session]);
  const [busy, setBusy] = useState(false);
  const [authAction, setAuthAction] = useState<"in" | "out">("in");
  const explicitFocus = useRef(false);
  useSessionFocus(!!session, loading, busy, explicitFocus);
  const [logoutFailed, setLogoutFailed] = useState(false);
  const [error, setError] = useState(() =>
    runtime.authMode === "oidc" ? loginFailureFromSearch(location.search) : "",
  );
  const [diagnostic, setDiagnostic] = useState<{
    signal: AbortSignal;
    error: DiagnosticError;
  }>();
  // Document navigation deliberately uses links: native unload protection must
  // run before an editor loses unsaved state, including back/forward navigation.
  // Catalog owns same-document detail/filter history and guards its drafts.
  // Ordinary links retain native unload protection.
  const [pathname, setPathname] = useState(location.pathname);
  const [fragment, setFragment] = useState(location.hash);
  useEffect(() => {
    const moved = (event: PopStateEvent) => {
      // Consult the mounted private-case guard before a route update can remove its editor.
      const changing = new CustomEvent("catalog-ui:navigate", {
        detail: event.state as unknown,
        cancelable: true,
      });
      if (window.dispatchEvent(changing)) {
        setPathname(location.pathname);
        setFragment(location.hash);
      }
    };
    const fragmentChanged = () => setFragment(location.hash);
    window.addEventListener("popstate", moved);
    window.addEventListener("hashchange", fragmentChanged);
    return () => {
      window.removeEventListener("popstate", moved);
      window.removeEventListener("hashchange", fragmentChanged);
    };
  }, []);
  const [section, id = ""] = pathname
    .slice(runtime.basePath.length + 1)
    .split("/");
  const view = section || "catalog";
  const helpPage =
    view === "about" || view === "getting-started" || view === "troubleshooting"
      ? view
      : undefined;
  const identityKey = session
    ? JSON.stringify([
        session.access.tenantId,
        session.access.realmId,
        session.access.membershipId,
        session.access.subject,
        session.access.actorType,
      ])
    : "";
  const policyKey = session ? JSON.stringify(session.access) : "";
  const page = session
    ? pageContext(session.access, view, id, fragment)
    : undefined;
  useEffect(() => {
    document.title = `${loading && !helpPage ? "Loading" : (page?.title ?? (helpPage === "about" ? "About" : helpPage === "troubleshooting" ? "Troubleshooting" : helpPage ? "Getting started" : "Sign in"))} | Open Agentic Catalog`;
  }, [loading, page?.title, helpPage]);
  const AuthoringScreen = Object.hasOwn(authoringScreens, view)
    ? authoringScreens[view as keyof typeof authoringScreens]
    : undefined;
  const ManagementScreen = Object.hasOwn(managementScreens, view)
    ? managementScreens[view as keyof typeof managementScreens]
    : undefined;
  const [users, setUsers] = useState<LoginOptions["users"]>([]);
  const [optionsAttempt, setOptionsAttempt] = useState(0);
  const [optionsBusy, setOptionsBusy] = useState(true);
  const [optionsError, setOptionsError] = useState("");
  const identitySignal = client.signal;
  useEffect(() => {
    setDiagnostic(undefined);
  }, [identitySignal]);
  const [theme, setTheme] = useState(() => preference("theme", "system"));
  const [density, setDensity] = useState(() =>
    preference("density", "comfortable"),
  );
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    remember("theme", theme);
  }, [theme]);
  useEffect(() => {
    document.documentElement.dataset.density = density;
    remember("density", density);
  }, [density]);
  useEffect(() => {
    let current = true;
    const signal = client.signal;
    client
      .auth("session")
      .then((value) => {
        if (current && !signal.aborted && value.access) setSession(value);
      })
      .catch((e) => {
        if (
          current &&
          !signal.aborted &&
          !(e instanceof APIError && e.status === 401)
        )
          setError(
            "Your session could not be checked. Reload the page to try again.",
          );
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
      client.invalidate();
    };
  }, [client]);
  useEffect(() => {
    let current = true;
    setOptionsBusy(true);
    setOptionsError("");
    setUsers([]);
    client
      .auth("options")
      .then((value) => {
        if (current && !identitySignal.aborted) setUsers(value.users);
      })
      .catch(() => {
        if (current && !identitySignal.aborted)
          setOptionsError(
            "Sign-in options could not be loaded. Reload sign-in options to try again.",
          );
      })
      .finally(() => {
        if (current && !identitySignal.aborted) setOptionsBusy(false);
      });
    return () => {
      current = false;
    };
  }, [client, identitySignal, optionsAttempt]);
  useEffect(() => {
    const channel = new BroadcastChannel(`catalog-ui:${runtime.basePath}`);
    channel.onmessage = (event: MessageEvent<{ sender?: string }>) => {
      if (event.data?.sender === tabID) return;
      explicitFocus.current = false;
      client.invalidate();
      setSession(undefined);
      setBusy(false);
      setError("Your session changed in another tab. Sign in to continue.");
    };
    return () => channel.close();
  }, [client, runtime.basePath, tabID]);
  const [expiryWarning, setExpiryWarning] = useState(false);
  const expiresAt = session?.expiresAt;
  useEffect(() => {
    setExpiryWarning(false);
    if (expiresAt === undefined) return;
    const remaining = expiresAt * 1000 - Date.now();
    if (remaining <= 120_000) setExpiryWarning(true);
    const warning =
      remaining > 120_000
        ? setTimeout(() => setExpiryWarning(true), remaining - 120_000)
        : undefined;
    return () => clearTimeout(warning);
  }, [expiresAt]);
  useEffect(() => {
    if (expiresAt === undefined) return;
    const timeout = setTimeout(
      () => {
        explicitFocus.current = false;
        client.invalidate();
        setSession(undefined);
        setBusy(false);
        setError("Your session expired. Sign in to continue.");
      },
      Math.max(0, Math.min(2 ** 31 - 1, expiresAt * 1000 - Date.now())),
    );
    return () => clearTimeout(timeout);
  }, [client, expiresAt]);
  const broadcast = () => {
    const channel = new BroadcastChannel(`catalog-ui:${runtime.basePath}`);
    channel.postMessage({ sender: tabID });
    channel.close();
  };
  // Invalidate earlier requests before provider navigation or development session replacement.
  async function signIn(alias?: string) {
    explicitFocus.current = true;
    setAuthAction("in");
    setBusy(true);
    setError("");
    client.invalidate();
    const signal = client.signal;
    setSession(undefined);
    broadcast();
    try {
      const reply = await client.auth("login", "POST", alias ? { alias } : {});
      if (signal.aborted) return;
      if (runtime.authMode === "oidc") {
        location.assign(reply.location);
        return;
      }
      const next = await client.auth("session");
      if (signal.aborted) return;
      if (!next.access) throw new APIError(401, "login_required");
      setSession(next);
      setLogoutFailed(false);
    } catch (e) {
      if (!signal.aborted)
        setError(
          e instanceof APIError
            ? e.message
            : "Sign-in failed. Please try again.",
        );
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  }
  // Clear private state immediately and retain explicit recovery if cookie deletion fails.
  async function signOut() {
    if (
      !window.dispatchEvent(
        new Event("catalog-ui:leave", { cancelable: true }),
      ) &&
      !confirm(
        "Sign out and discard unsaved input or an unresolved command? A request already sent may still complete.",
      )
    )
      return;
    explicitFocus.current = true;
    setAuthAction("out");
    setBusy(true);
    setError("");
    client.invalidate();
    const signal = client.signal;
    setSession(undefined);
    broadcast();
    try {
      await client.auth("logout", "POST", {});
      if (!signal.aborted) setLogoutFailed(false);
    } catch {
      if (!signal.aborted) {
        setLogoutFailed(true);
        setError(
          "Sign-out could not reach the server. The session cookie may still be active. Retry sign-out before leaving a shared browser.",
        );
      }
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  }
  const failure = (e: unknown) => {
    const safe = diagnosticError(e);
    if (safe && safe.status !== 401)
      setDiagnostic({ signal: client.signal, error: safe });
    if (e instanceof APIError && e.status === 401) {
      explicitFocus.current = false;
      client.invalidate();
      setSession(undefined);
      setBusy(false);
      setError(e.message);
    }
    // Reconcile stale role hints once for concurrent denials. Do not replay the
    // denied operation: the screen retains its error and any prepared command.
    if (
      e instanceof APIError &&
      e.status === 403 &&
      refreshingAccess.current !== client.signal
    ) {
      const signal = client.signal;
      refreshingAccess.current = signal;
      client
        .auth("session")
        .then((next) => {
          if (signal.aborted) return;
          if (next.access) setSession(next);
          else {
            client.invalidate();
            setSession(undefined);
          }
        })
        .catch((error: unknown) => {
          // Current revoked/invalid identity clears private state. A temporary
          // outage retains the denied operation and never grants new hints.
          if (
            !signal.aborted &&
            error instanceof APIError &&
            (error.status === 401 || error.code === "invalid_session_response")
          ) {
            client.invalidate();
            setSession(undefined);
            setBusy(false);
            setError(error.message);
          }
        })
        .finally(() => {
          if (refreshingAccess.current === signal)
            refreshingAccess.current = undefined;
        });
    }
  };
  return (
    <>
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="topbar" ref={header}>
        <a className="brand" href={`${runtime.basePath}/catalog`}>
          <span className="brand-mark" aria-hidden="true">
            <Icon name="catalog" size={21} />
          </span>{" "}
          <span>
            Open Agentic <strong>Catalog</strong>
          </span>
        </a>
        <span className="topbar-label">Discover &amp; connect</span>
        {runtime.authMode === "development" && (
          <span className="environment-label">
            <span />
            Local development
          </span>
        )}
        <div className="header-actions">
          {session && (
            <QuickNavigation
              access={session.access}
              basePath={runtime.basePath}
            />
          )}
          <details
            className="appearance"
            ref={preferences}
            onBlur={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget))
                event.currentTarget.open = false;
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape" && preferences.current) {
                preferences.current.open = false;
                preferences.current.querySelector("summary")?.focus();
              }
            }}
          >
            <summary
              aria-label="Appearance"
              title="Appearance"
              onClick={() => {
                if (account.current) account.current.open = false;
              }}
            >
              <Icon name="sun" />
              <span>Appearance</span>
              <Icon name="chevron" size={12} />
            </summary>
            <div className="appearance-panel">
              <h2>Appearance</h2>
              <p>Make this workspace comfortable for you.</p>
              <fieldset className="appearance-options">
                <legend>Theme</legend>
                <div className="theme-options">
                  {(
                    [
                      ["system", "System", "monitor"],
                      ["light", "Light", "sun"],
                      ["dark", "Dark", "moon"],
                    ] as const
                  ).map(([value, label, icon]) => (
                    <label key={value} className="appearance-choice">
                      <input
                        type="radio"
                        name="theme"
                        value={value}
                        checked={theme === value}
                        onChange={() => setTheme(value)}
                      />
                      <span className={`theme-preview theme-preview-${value}`}>
                        <Icon name={icon} size={20} />
                      </span>
                      <span>{label}</span>
                    </label>
                  ))}
                </div>
              </fieldset>
              <fieldset className="appearance-options">
                <legend>Density</legend>
                <div className="density-options">
                  {(["comfortable", "compact"] as const).map((value) => (
                    <label key={value} className="appearance-choice">
                      <input
                        type="radio"
                        name="density"
                        value={value}
                        checked={density === value}
                        onChange={() => setDensity(value)}
                      />
                      <span>
                        {value === "compact" ? "Compact" : "Comfortable"}
                      </span>
                    </label>
                  ))}
                </div>
              </fieldset>
              <p className="appearance-note">
                Saved in this browser. System follows your device theme.
              </p>
            </div>
          </details>
          {session && (
            <details
              className="account"
              ref={account}
              onBlur={(event) => {
                if (!event.currentTarget.contains(event.relatedTarget))
                  event.currentTarget.open = false;
              }}
              onKeyDown={(event) => {
                if (event.key === "Escape" && account.current?.open) {
                  account.current.open = false;
                  account.current.querySelector("summary")?.focus();
                }
              }}
            >
              <summary
                title="Account"
                onClick={() => {
                  // Appearance shares this mounted header and always has its ref.
                  preferences.current!.open = false;
                }}
              >
                <Icon name="user" />
                <span>Account</span>{" "}
                <span className="account-subject">
                  {session.access.subject}
                </span>
                <Icon name="chevron" size={12} />
              </summary>
              <div className="account-panel">
                <div className="account-identity">
                  <strong>{session.access.subject}</strong>
                  <span className="muted">
                    {session.access.actorType === "human"
                      ? "Human account"
                      : "Service identity"}
                  </span>
                </div>
                <nav aria-label="Account pages">
                  {navigationGroups(session.access)
                    .filter((group) => group.placement === "account")
                    .flatMap((group) => group.entries)
                    .filter((entry) => entry.allowed)
                    .map((entry) => (
                      <a
                        key={entry.path}
                        href={`${runtime.basePath}/${entry.path}`}
                        aria-current={
                          page?.current === entry.path ? "page" : undefined
                        }
                      >
                        <Icon name={entry.icon} />
                        {entry.label}
                      </a>
                    ))}
                </nav>
                <div className="account-signout">
                  <button disabled={busy} onClick={() => void signOut()}>
                    <Icon name="logout" size={16} />
                    Sign out
                  </button>
                </div>
              </div>
            </details>
          )}
        </div>
      </header>
      {loading && !helpPage ? (
        <main id="main">
          <p role="status">Loading your workspace...</p>
        </main>
      ) : !session && helpPage ? (
        <main id="main" className="public-help">
          <PublicLinks runtime={runtime} />
          <Suspense fallback={<p role="status">Loading Help...</p>}>
            <Help
              page={helpPage}
              runtime={runtime}
              client={client}
              access={undefined}
              onFailure={failure}
            />
          </Suspense>
        </main>
      ) : !session ? (
        <main id="main" className="login">
          <div className="login-symbols" aria-hidden="true">
            {kindChoices.map(({ value }) => (
              <span className={`kind-icon ${value}`} key={value}>
                <Icon name={value} size={28} />
              </span>
            ))}
          </div>
          <p className="eyebrow">ONE PLACE TO EXPLORE</p>
          <h1 tabIndex={-1}>Find Agents, MCP Servers, Skills and Plugins.</h1>
          <p>
            Browse governed Agents, MCP Servers, Skills and Plugins, with their
            publication and source information.
          </p>
          {error && (
            <p role="alert" className="notice error">
              {error}
            </p>
          )}
          {logoutFailed && (
            <button disabled={busy} onClick={() => void signOut()}>
              Retry sign-out
            </button>
          )}
          <PublicLinks runtime={runtime} />
          <section className="login-card">
            <h2>Sign in</h2>
            {runtime.authMode === "development" ? (
              <>
                <p className="muted">
                  Local development identities. Permissions are enforced by the
                  Catalog.
                </p>
                {optionsBusy && <p role="status">Loading sign-in options...</p>}
                {optionsError && (
                  <>
                    <p role="alert">{optionsError}</p>
                    <button
                      disabled={busy || optionsBusy}
                      onClick={() => setOptionsAttempt((value) => value + 1)}
                    >
                      {controlIcons["refresh"]} Reload sign-in options
                    </button>
                  </>
                )}
                <SignInChoices
                  users={users}
                  busy={busy}
                  onSelect={(alias) => void signIn(alias)}
                />
              </>
            ) : (
              <>
                <p>Use your account to access the Catalog.</p>
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => void signIn()}
                >
                  Sign in
                </button>
              </>
            )}
            {busy && (
              <p role="status">
                {authAction === "out" ? "Signing out..." : "Signing in..."}
              </p>
            )}
          </section>
        </main>
      ) : (
        <InvestigationProvider key={identityKey} policyKey={policyKey}>
          <div className="workspace" ref={workspace}>
            <aside
              className="sidebar"
              data-menu-open={mobileMenuOpen}
              onKeyDown={(event) => {
                if (event.key === "Escape" && mobileMenuOpen) {
                  setMobileMenuOpen(false);
                  menuToggle.current?.focus();
                }
              }}
            >
              <button
                ref={menuToggle}
                className="mobile-nav-toggle"
                type="button"
                aria-expanded={mobileMenuOpen}
                aria-controls="workspace-navigation"
                title="Show or hide workspace pages"
                onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
              >
                <Icon name="list" />
                Workspace menu
              </button>
              <nav id="workspace-navigation" aria-label="Workspace">
                {navigationGroups(session.access)
                  .filter((group) => group.placement === "sidebar")
                  .map((group) => {
                    const entries = group.entries.filter(
                      (entry) => entry.allowed,
                    );
                    return entries.length ? (
                      <div
                        className="nav-group"
                        role="group"
                        aria-label={group.label}
                        key={group.key}
                      >
                        <span className="nav-label">{group.label}</span>
                        {group.key === "help" &&
                          documentationLink(runtime.documentationUrl) && (
                            <a
                              href={documentationLink(runtime.documentationUrl)}
                              target="_blank"
                              rel="noopener noreferrer"
                              aria-label="Documentation"
                              aria-describedby="documentation-new-tab"
                            >
                              <Icon name="book" />
                              Documentation
                              <span
                                id="documentation-new-tab"
                                className="sr-only"
                              >
                                Opens in a new tab
                              </span>
                            </a>
                          )}
                        {entries.map((entry) => (
                          <a
                            key={entry.path}
                            href={`${runtime.basePath}/${entry.path}`}
                            aria-current={
                              page?.current === entry.path ? "page" : undefined
                            }
                          >
                            <Icon name={entry.icon} />
                            {entry.label}
                          </a>
                        ))}
                      </div>
                    ) : null;
                  })}
              </nav>
            </aside>
            <main id="main">
              {page && (
                <nav className="breadcrumb" aria-label="Breadcrumb">
                  <span>{page.group}</span>
                  <Icon name="chevron" size={12} />
                  {page.parent && (
                    <>
                      <a href={`${runtime.basePath}/${page.parent.path}`}>
                        {page.parent.label}
                      </a>
                      <Icon name="chevron" size={12} />
                    </>
                  )}
                  <span aria-current="page">{page.title}</span>
                </nav>
              )}
              {expiryWarning && (
                <p role="status" className="notice">
                  Your session expires within two minutes. Expiry clears unsaved
                  input. Finish an explicit save where possible, then sign in
                  again after expiry.
                </p>
              )}
              <OperationProvider
                key={identityKey}
                access={session.access}
                runtime={runtime}
                view={view}
              >
                {helpPage ? (
                  <Suspense fallback={<p role="status">Loading Help...</p>}>
                    <Help
                      page={helpPage}
                      runtime={runtime}
                      client={client}
                      access={session.access}
                      onFailure={failure}
                      recent={
                        diagnostic?.signal === client.signal
                          ? diagnostic.error
                          : undefined
                      }
                    />
                  </Suspense>
                ) : ManagementScreen ? (
                  <Suspense
                    fallback={<p role="status">Loading management...</p>}
                  >
                    <ManagementScreen
                      client={client}
                      runtime={runtime}
                      access={session.access}
                      id={id}
                      onFailure={failure}
                    />
                  </Suspense>
                ) : view === "my-work" ? (
                  session.access.actorType === "human" &&
                  (!session.access.scopeEnforcement ||
                    session.access.scopes?.includes("catalog.read")) ? (
                    <Suspense
                      fallback={
                        <p role="status">Loading personal workspace...</p>
                      }
                    >
                      <MyWork
                        key={policyKey}
                        client={client}
                        runtime={runtime}
                        access={session.access}
                        id={id}
                        onFailure={failure}
                      />
                    </Suspense>
                  ) : (
                    <PermissionDenied
                      title={page!.title}
                      runtime={runtime}
                      description="This page is unavailable with your current access. Check My access for your permissions and token scopes."
                    />
                  )
                ) : [
                    "catalog",
                    "explore",
                    "indexed-record",
                    "saved-items",
                  ].includes(view) ? (
                  <EditorAdmission
                    key={identityKey}
                    target={
                      view === "saved-items" ? `${view}/${id}` : "discovery"
                    }
                    allowed={
                      view === "saved-items"
                        ? session.access.actorType === "human" &&
                          permits(session.access, "library.own", "catalog.read")
                        : view === "explore"
                          ? canReadLocal(session.access)
                          : view === "indexed-record"
                            ? permits(
                                session.access,
                                "federation.discover",
                                "catalog.read",
                              )
                            : canRead(session.access)
                    }
                    retain={view === "saved-items" && !!id}
                    title={page!.title}
                    runtime={runtime}
                    fallback={
                      view === "catalog" || view === "explore" ? (
                        <Suspense
                          fallback={<p role="status">Loading access...</p>}
                        >
                          <Access
                            runtime={runtime}
                            access={session.access}
                            client={client}
                            onFailure={failure}
                          />
                        </Suspense>
                      ) : undefined
                    }
                  >
                    <Suspense
                      fallback={<p role="status">Loading Catalog...</p>}
                    >
                      <DiscoveryWorkspace
                        client={client}
                        runtime={runtime}
                        access={session.access}
                        id={id}
                        onFailure={failure}
                      />
                    </Suspense>
                  </EditorAdmission>
                ) : AuthoringScreen ? (
                  <EditorAdmission
                    key={JSON.stringify([identityKey, view, id])}
                    allowed={
                      view === "reviews" || view === "releases"
                        ? availablePermission(
                            session.access,
                            "resource.read_history",
                            "catalog.read",
                          )
                        : view === "version-revisions" ||
                            view === "listing-revisions"
                          ? availablePermission(
                              session.access,
                              "resource.read_content",
                              "catalog.read",
                            )
                          : availablePermission(
                              session.access,
                              "resource.read",
                              "catalog.read",
                            ) ||
                            (view === "resources" &&
                              (!id || id === "new") &&
                              availablePermission(
                                session.access,
                                "resource.create",
                                "catalog.write",
                              ))
                    }
                    retain={view === "resources" && id === "new"}
                    title={page!.title}
                    runtime={runtime}
                  >
                    <Suspense
                      fallback={<p role="status">Loading workflow...</p>}
                    >
                      <AuthoringScreen
                        client={client}
                        runtime={runtime}
                        access={session.access}
                        id={id}
                        onFailure={failure}
                      />
                    </Suspense>
                  </EditorAdmission>
                ) : view === "namespaces" ? (
                  <Suspense
                    fallback={<p role="status">Loading namespaces...</p>}
                  >
                    <Namespaces
                      client={client}
                      runtime={runtime}
                      access={session.access}
                      id={id}
                      onFailure={failure}
                    />
                  </Suspense>
                ) : view === "publishers" ? (
                  <Suspense
                    fallback={<p role="status">Loading publishers...</p>}
                  >
                    <Publishers
                      client={client}
                      runtime={runtime}
                      access={session.access}
                      id={id}
                      onFailure={failure}
                    />
                  </Suspense>
                ) : view === "jobs" ? (
                  <Suspense fallback={<p role="status">Loading jobs...</p>}>
                    <Jobs
                      client={client}
                      runtime={runtime}
                      access={session.access}
                      id={id}
                      onFailure={failure}
                    />
                  </Suspense>
                ) : view === "api-reference" ? (
                  <Suspense
                    fallback={<p role="status">Loading API reference...</p>}
                  >
                    <APIReference
                      client={client}
                      onFailure={failure}
                      basePath={runtime.basePath}
                    />
                  </Suspense>
                ) : view === "service" ? (
                  <Suspense
                    fallback={
                      <p role="status">Loading service information...</p>
                    }
                  >
                    <Service
                      client={client}
                      runtime={runtime}
                      access={session.access}
                      onFailure={failure}
                    />
                  </Suspense>
                ) : (
                  <Suspense fallback={<p role="status">Loading access...</p>}>
                    <Access
                      runtime={runtime}
                      access={session.access}
                      client={client}
                      onFailure={failure}
                    />
                  </Suspense>
                )}
              </OperationProvider>
            </main>
          </div>
        </InvestigationProvider>
      )}
    </>
  );
}

/** Preserve only an already-entered editor; its own current read and action
 * guards remain authoritative. The identity/target key discards its admission
 * on replacement, and an initially denied route never mounts private content.
 */
function EditorAdmission({
  allowed,
  target = "editor",
  fallback,
  retain,
  title,
  runtime,
  children,
}: {
  allowed: boolean;
  target?: string;
  fallback?: ReactNode;
  retain: boolean;
  title: string;
  runtime: Runtime;
  children: ReactNode;
}) {
  const [entered, setEntered] = useState({ target, allowed });
  if (entered.target !== target || (allowed && !entered.allowed))
    setEntered({ target, allowed });
  return allowed || (retain && entered.target === target && entered.allowed)
    ? children
    : (fallback ?? (
        <PermissionDenied
          title={title}
          runtime={runtime}
          description="This page is unavailable with your current access. Check My access for your permissions and token scopes."
        />
      ));
}

/** Read an appearance preference while tolerating unavailable browser storage. */
function preference(name: string, fallback: string): string {
  try {
    return localStorage.getItem(`catalog-ui:${name}`) ?? fallback;
  } catch {
    return fallback;
  }
}
/** Persist appearance only; blocked browser storage must not interrupt the workspace. */
function remember(name: string, value: string) {
  try {
    localStorage.setItem(`catalog-ui:${name}`, value);
  } catch {
    /* Appearance storage is optional. */
  }
}

/** Public links use only the validated host destination and fixed local pages. */
function PublicLinks({ runtime }: { runtime: Runtime }) {
  const documentation = documentationLink(runtime.documentationUrl);
  return (
    <nav className="public-links" aria-label="Public Help">
      <a href={`${runtime.basePath}/catalog`}>Sign in</a>
      <a href={`${runtime.basePath}/getting-started`}>Getting started</a>
      <a href={`${runtime.basePath}/about`}>About</a>
      <a href={`${runtime.basePath}/troubleshooting`}>Troubleshooting</a>
      {documentation && (
        <a
          href={documentation}
          aria-label="Documentation"
          aria-describedby="public-documentation-new-tab"
          target="_blank"
          rel="noopener noreferrer"
        >
          {controlIcons["external"]} Documentation
          <span id="public-documentation-new-tab" className="sr-only">
            Opens in a new tab
          </span>
        </a>
      )}
    </nav>
  );
}
