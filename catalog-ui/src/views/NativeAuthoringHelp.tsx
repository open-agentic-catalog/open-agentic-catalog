import type { Kind } from "../api/client";
import { documentationGuideLink } from "../api/session";

const guides = {
  agent: "agent-authoring",
  mcp_server: "mcp-authoring",
  skill: "skill-authoring",
  plugin: "plugin-example",
} as const;
const startingPoints = {
  agent: "Agent details or an existing Card",
  mcp_server: "Hosted Server Card or Registry distribution metadata",
  skill: "SKILL.md or a complete Skill package",
  plugin: "Plugin fields or a complete HTTPS archive",
} as const;

/** Kind-specific preparation guidance; it never loads an example or changes input. */
export function NativeAuthoringHelp({
  kind,
  documentationUrl,
}: {
  kind: Kind;
  documentationUrl: string;
}) {
  const guide = documentationGuideLink(documentationUrl, guides[kind]);
  return (
    <section
      className="native-authoring-help"
      aria-label="Native input guidance"
    >
      <details>
        <summary>
          <strong>Prepare native content: </strong>
          {startingPoints[kind]}.
        </summary>
        {kind === "agent" && (
          <p>
            Describe the Agent in the labelled fields. The Catalog creates the
            A2A Agent Card. Use Advanced JSON or import a Card for other fields.
            The Catalog does not invoke advertised endpoints.
          </p>
        )}
        {kind === "mcp_server" && (
          <p>
            Choose Hosted server for a primary experimental MCP Server Card.
            Choose Package or distribution metadata for MCP Registry
            server.json. Registry metadata alone does not supply ARD or AI
            Catalog snapshot content. Keep it in another row when a Server Card
            is primary. Changing a label does not convert a document.
          </p>
        )}
        {kind === "skill" && (
          <p>
            Enter the Skill name, description and Markdown instructions, or
            upload a complete ZIP or gzipped-tar Skill directory with all
            supporting files. A complete package requires a Git commit or OCI
            digest handoff. The Catalog retains its inventory and digest, not
            archive bytes; it does not run the Skill.
          </p>
        )}
        {kind === "plugin" ? (
          <p>
            Choose Prepare Plugin files to generate a manifest and optional
            Skill or MCP files from fields. Package and host those files
            yourself, or use an existing credential-free HTTPS ZIP or
            gzipped-tar archive. Include plugin.json and every referenced
            component. Local manifests and archive uploads are not supported.
            Prefer a versioned archive and its expected SHA-256 checksum. The
            Catalog inspects the package without installing or executing it.
          </p>
        ) : kind === "agent" || kind === "mcp_server" ? (
          <p>
            Choose Import an existing {kind === "agent" ? "Agent" : "MCP"}{" "}
            document for a local UTF-8 JSON file. Loading and editing stay in
            the browser. An anonymous HTTPS document is retrieved only after you
            confirm.
          </p>
        ) : (
          <p>
            Load a local document or paste its contents into the appropriate
            editor. An anonymous HTTPS document is a separate input; the backend
            retrieves it only after you confirm. Switching input modes asks
            before discarding entered content or a staged file.
          </p>
        )}
        <p>
          Where native metadata declares a version, it must match Version label.
          Include Listing details before the final confirmation, or prepare them
          after saving content. Inspect the returned validation findings before
          review. Native content and Listing display metadata are separate.
          Saving does not publish or execute the resource.
        </p>
      </details>
      {guide && (
        <p>
          <a href={guide.href} target="_blank" rel="noopener noreferrer">
            {guide.label} (opens in a new tab)
          </a>
        </p>
      )}
    </section>
  );
}
