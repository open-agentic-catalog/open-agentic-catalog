import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
import { result, canRead } from "../api/client";
import type { Access, Client, Runtime } from "../api/client";
import { availablePermission, permits } from "../api/permissions";
import { documentationLink } from "../api/session";
import { Heading, ReadState, useRead } from "./workspace";
import * as checks from "./responses";
import Troubleshooting from "./Troubleshooting";
import type { DiagnosticError } from "./diagnostic-details";

export default function Help({
  page,
  runtime,
  client,
  access,
  onFailure,
  recent,
}: {
  page: "about" | "getting-started" | "troubleshooting";
  runtime: Runtime;
  client: Client;
  access: Access | undefined;
  onFailure: (error: unknown) => void;
  recent?: DiagnosticError | undefined;
}) {
  if (page === "troubleshooting")
    return (
      <Troubleshooting
        runtime={runtime}
        client={client}
        access={access}
        onFailure={onFailure}
        recent={recent}
      />
    );
  return page === "about" ? (
    <>
      <Heading
        title="About"
        icon="catalog"
        description="Open Agentic Catalog"
      />
      <section className="workspace-panel">
        <h2>Find, review and share AI resource descriptions.</h2>
        <p>
          Find, describe, review and publish Agents, MCP Servers, Skills and
          Plugins. See their versions, review decisions and where their
          descriptions came from.
        </p>
        <ul>
          <li>
            {controlIcons["agent"]} <strong>Agents</strong> describe an agent's
            capabilities and supported interfaces.
          </li>
          <li>
            {controlIcons["mcp_server"]} <strong>MCP Servers</strong> describe
            servers that expose tools and other context through the Model
            Context Protocol.
          </li>
          <li>
            {controlIcons["skill"]} <strong>Skills</strong> contain reusable
            instructions and supporting files.
          </li>
          <li>
            {controlIcons["plugin"]} <strong>Plugins</strong> group supported
            components in a versioned package.
          </li>
        </ul>
        <p>
          Discovery does not grant trust, approval or permission to run a
          resource. Use your own client or runtime for installation and
          execution.
        </p>
        <a href={`${runtime.basePath}/getting-started`}>
          {controlIcons["book"]} Getting started
        </a>
      </section>
      <section className="workspace-panel">
        <h2>License and notices</h2>
        <p>Apache License 2.0</p>
        <p>Author: Mihai Criveti</p>
        <ul>
          <li>
            <a href={`${runtime.basePath}/legal/LICENSE.txt`}>
              Project license
            </a>
          </li>
          <li>
            <a href={`${runtime.basePath}/legal/third-party-notices.txt`}>
              Browser third-party notices
            </a>
          </li>
        </ul>
      </section>
      {access && (
        <BuildObservations
          key={JSON.stringify(access)}
          client={client}
          runtime={runtime}
          onFailure={onFailure}
        />
      )}
    </>
  ) : (
    <GettingStarted runtime={runtime} access={access} />
  );
}

function BuildObservations({
  client,
  runtime,
  onFailure,
}: {
  client: Client;
  runtime: Runtime;
  onFailure: (error: unknown) => void;
}) {
  const api = useRead(
    "about-api-build",
    async (signal) =>
      result(
        await client.api.GET("/api/v1/service-information", { signal }),
        checks.ServiceInformation,
      ),
    onFailure,
  );
  const ui = useRead(
    "about-ui-build",
    (signal) => client.buildInfo(signal),
    onFailure,
  );
  return (
    <section className="workspace-panel">
      <h2>Connected builds</h2>
      <p>These are the API and UI host builds that answered this browser.</p>
      <ReadState {...api} />
      <ReadState {...ui} />
      <Facts
        rows={[
          ...(api.data ? [["API version", api.data.version] as const] : []),
          ...(ui.data ? [["UI host version", ui.data.version] as const] : []),
        ]}
      />
      <details>
        <summary>Build fingerprints</summary>
        <Facts
          rows={[
            ...(api.data
              ? [
                  [
                    "API commit / source state",
                    <>
                      <code>{api.data.commit}</code> / {api.data.sourceState}
                    </>,
                  ] as const,
                ]
              : []),
            ...(ui.data
              ? [
                  [
                    "UI commit / source state",
                    <>
                      <code>{ui.data.commit}</code> / {ui.data.sourceState}
                    </>,
                  ] as const,
                ]
              : []),
          ]}
        />
      </details>
      <a href={`${runtime.basePath}/service`}>Service information</a>
    </section>
  );
}

function GettingStarted({
  runtime,
  access,
}: {
  runtime: Runtime;
  access: Access | undefined;
}) {
  const guide = documentationLink(runtime.documentationUrl);
  const workflows = [
    {
      guide: "find-and-inspect-resources",
      title: "Find and use a resource",
      description:
        "Search the Catalog, inspect the exact version and source, then copy its metadata or follow its handoff instructions in your own client.",
      path: "catalog",
      label: "Open Catalog",
      icon: controlIcons["catalog"],
      allowed: !!access && canRead(access),
      prerequisite:
        "Requires local resource.read or federation.discover access and catalog.read when scopes are enforced.",
    },
    {
      guide: "author-review-and-publish",
      title: "Contribute a resource",
      description:
        "Create a Resource in an allowed namespace. Add a Version, validate its representation, then prepare a Listing and submit it for independent review.",
      path: "resources",
      label: "Open Authoring",
      icon: controlIcons["book"],
      allowed:
        !!access &&
        (availablePermission(access, "resource.create", "catalog.write") ||
          availablePermission(access, "resource.edit", "catalog.write")) &&
        (availablePermission(access, "resource.read", "catalog.read") ||
          availablePermission(access, "resource.create", "catalog.write")),
      prerequisite:
        "Requires resource.create or resource.edit in the destination namespace, the relevant reads and catalog.write when scopes are enforced.",
    },
    {
      guide: "author-review-and-publish",
      title: "Review a submission",
      description:
        "Inspect the submitted Listing revision and its validation evidence. Approve it or request changes. A reviewer must be independent of the submitted work.",
      path: "reviews",
      label: "Open Reviews",
      icon: controlIcons["access"],
      allowed:
        !!access &&
        availablePermission(access, "review.decide", "catalog.review") &&
        availablePermission(access, "resource.read_history", "catalog.read"),
      prerequisite:
        "Requires review.decide and the submission's history/content access, plus catalog.review when scopes are enforced.",
    },
    {
      guide: "author-review-and-publish",
      title: "Publish an approved release",
      description:
        "Open an approved submission in Reviews to publish its exact revisions as a release. Discovery publication is a separate step that activates an audience-specific snapshot.",
      path: "reviews",
      label: "Open approved submissions",
      icon: controlIcons["publish"],
      allowed:
        !!access &&
        availablePermission(access, "release.publish", "catalog.publish") &&
        availablePermission(access, "resource.read_history", "catalog.read"),
      prerequisite:
        "Requires release.publish and submission history access, plus catalog.publish when scopes are enforced. Snapshot generation and activation have separate permissions and host policy.",
    },
    {
      guide: "tenant-operational-settings",
      title: "Configure this Catalog",
      description:
        "Use tenant Settings for supported overrides. Deployment operators configure sign-in, database connections and other startup settings outside the browser. Catalog does not manage identity-provider passwords.",
      path: "settings",
      label: "Open Settings",
      icon: controlIcons["settings"],
      allowed: !!access && permits(access, "settings.read", "tenant.admin"),
      prerequisite:
        "Requires settings.read and tenant.admin when scopes are enforced. Saving settings requires settings.manage. Ask your operator about deployment settings.",
    },
  ];
  return (
    <>
      <Heading
        title="Getting started"
        icon="book"
        description="Choose what you want to do."
      />
      {!access && (
        <p>
          Sign in to see the workflow links available to you. You can read this
          orientation without an account.
        </p>
      )}
      <div className="help-workflows">
        {workflows.map((flow) => (
          <section key={flow.path + flow.title} className="workspace-panel">
            <h2>{flow.title}</h2>
            <p>{flow.description}</p>
            {flow.allowed ? (
              <a
                className="button primary"
                href={`${runtime.basePath}/${flow.path}`}
              >
                {flow.icon} {flow.label}
              </a>
            ) : (
              <p className="muted">{flow.prerequisite}</p>
            )}
            {guide === "/docs/" && (
              <p>
                <a
                  href={`/docs/guides/catalog-ui/#${flow.guide}`}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  {controlIcons["external"]} Read this workflow
                  <span className="sr-only"> (opens in a new tab)</span>
                </a>
              </p>
            )}
          </section>
        ))}
      </div>
      <details className="workspace-advanced">
        <summary>How publication works</summary>{" "}
        <p>
          A Resource has Versions and validated evidence. A Listing describes a
          Version for independent review. An approved submission can become a
          release; an active discovery snapshot makes eligible releases
          discoverable to its audience.
        </p>
        <p>
          Each action checks current permissions, token scopes, configured
          capabilities and the selected namespace. A link does not grant access
          or guarantee that a particular record is eligible.
        </p>
      </details>
      {access && (
        <p>
          <a href={`${runtime.basePath}/access`}>
            {controlIcons["access"]} Check My access
          </a>{" "}
          for current permissions and assignment sources.
        </p>
      )}
      {guide ? (
        <p>
          <a
            href={guide === "/docs/" ? "/docs/guides/catalog-ui/" : guide}
            target="_blank"
            rel="noopener noreferrer"
          >
            {controlIcons["external"]} Read the UI workflow guide
            <span className="sr-only"> (opens in a new tab)</span>
          </a>
        </p>
      ) : (
        <p>
          The operator has not configured a documentation destination for this
          UI.
        </p>
      )}
      {guide === "/docs/" && (
        <p>
          <a
            href="/docs/operations/oidc-keycloak/#start-one-local-catalog-with-passwords"
            target="_blank"
            rel="noopener noreferrer"
          >
            {controlIcons["external"]} Set up local users and passwords
            <span className="sr-only"> (opens in a new tab)</span>
          </a>
        </p>
      )}
    </>
  );
}
