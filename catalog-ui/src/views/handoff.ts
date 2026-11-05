// Reference projection reads explicit metadata only; it never builds commands or contacts endpoints.
import type { components } from "../api/schema";

// Package evidence has different generated union shapes in operation responses
// and components; narrow its fields explicitly at the projection boundary.
export type Representation = Omit<
  components["schemas"]["Representation"],
  "package"
> & { package?: unknown };
export type Reference = { label: string; value: string };

/** Narrow extension/evidence objects without assigning meaning to unknown fields. */
export function object(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

/** Project only declared, already-loaded native facts; metadata URLs are not runtimes. */
export function representationDisplay(item: Representation): {
  delivery: string[];
  attribution: Reference[];
} {
  const delivery = new Set<string>();
  const attribution: Reference[] = [];
  const add = (label: string, value: unknown) => {
    if (typeof value !== "string" || value === "") return;
    attribution.push({
      label,
      value:
        [...value].length <= 256
          ? value
          : "See complete representation metadata for the declaration.",
    });
  };
  const handoff = object(item.handoff);
  if (
    typeof object(handoff.git).repository === "string" &&
    object(handoff.git).repository
  )
    delivery.add("Git source");
  if (
    typeof object(handoff.oci).reference === "string" &&
    object(handoff.oci).reference
  )
    delivery.add("OCI distribution");
  const data = object(item.data);
  if (item.profileId === "a2a-agent-card-1.0.1-3303592") {
    if (
      Array.isArray(data.supportedInterfaces) &&
      data.supportedInterfaces.some(
        (v) => typeof object(v).url === "string" && object(v).url,
      )
    )
      delivery.add("Declared remote interface");
    add("Declared provider", object(data.provider).organization);
  }
  if (
    [
      "mcp-registry-server-1.8.1-f52dc85",
      "mcp-server-card-v1-experimental-526201b",
    ].includes(item.profileId)
  ) {
    if (
      Array.isArray(data.remotes) &&
      data.remotes.some(
        (v) => typeof object(v).url === "string" && object(v).url,
      )
    )
      delivery.add("Declared remote interface");
    if (
      item.profileId === "mcp-registry-server-1.8.1-f52dc85" &&
      Array.isArray(data.packages) &&
      data.packages.some((v) => object(v).registryType === "oci")
    )
      delivery.add("OCI distribution");
  }
  if (item.profileId === "agent-skills-69ef37e")
    add("Declared native license", object(item.normalizedFacts).license);
  if (item.profileId === "agent-plugins-1.0.0-1fc1b627" && item.plugin) {
    delivery.add("Inspected Plugin archive");
    const root = item.plugin.rootDirectory;
    const manifest =
      typeof root === "string"
        ? item.plugin.documents.find(
            (v) =>
              v.path ===
              (root && root !== "." ? root + "/plugin.json" : "plugin.json"),
          )
        : undefined;
    if (
      manifest &&
      typeof manifest.content === "string" &&
      new TextEncoder().encode(manifest.content).length <= 500_000
    ) {
      try {
        const value: unknown = JSON.parse(manifest.content);
        const declared = object(value);
        add("Declared author", object(declared.author).name);
        add("Declared native license", declared.license);
      } catch {
        /* Unavailable declarations do not invalidate the retained raw evidence. */
      }
    }
  }
  return { delivery: [...delivery], attribution };
}

/** Retain bounded literal strings for display/copy, not executable links or shell commands. */
function text(value: unknown): string {
  return typeof value === "string" && value.length <= 8192 ? value : "";
}

/** Project exact declared Git/OCI pins and supported native endpoint metadata. */
export function representationReferences(item: Representation): Reference[] {
  const fields: Reference[] = [];
  const add = (label: string, value: unknown) => {
    const literal = text(value);
    if (literal) fields.push({ label, value: literal });
  };
  add(
    item.plugin || item.package
      ? "External archive URL"
      : "Metadata document URL",
    item.url,
  );
  const handoff = object(item.handoff);
  const git = object(handoff.git);
  add("Git repository", git.repository);
  add("Git commit", git.commit);
  add("Git directory", git.directory);
  add("OCI digest reference", object(handoff.oci).reference);
  const data = object(item.data);
  const a2a = item.profileId === "a2a-agent-card-1.0.1-3303592";
  const mcp = [
    "mcp-registry-server-1.8.1-f52dc85",
    "mcp-server-card-v1-experimental-526201b",
  ].includes(item.profileId);
  const endpoints = a2a
    ? data.supportedInterfaces
    : mcp
      ? data.remotes
      : undefined;
  if (Array.isArray(endpoints)) {
    endpoints.slice(0, 16).forEach((value, index) => {
      const endpoint = object(value);
      add(`Declared endpoint ${index + 1}`, endpoint.url);
      add(
        `Endpoint ${index + 1} transport`,
        a2a ? endpoint.protocolBinding : endpoint.type,
      );
      if (a2a)
        add(`Endpoint ${index + 1} protocol version`, endpoint.protocolVersion);
    });
    if (endpoints.length > 16)
      fields.push({
        label: "Endpoint limit",
        value:
          "First 16 shown; inspect the retained representation for the complete list.",
      });
  }
  return fields;
}

/** Describe authentication declarations without copying credentials, header values or defaults. */
export function authenticationSummary(item: Representation): string {
  if (
    !item.data ||
    ![
      "a2a-agent-card-1.0.1-3303592",
      "mcp-registry-server-1.8.1-f52dc85",
      "mcp-server-card-v1-experimental-526201b",
    ].includes(item.profileId)
  )
    return "";
  const data = object(item.data);
  if (
    item.profileId === "a2a-agent-card-1.0.1-3303592" &&
    (data.securitySchemes || data.securityRequirements)
  )
    return "Authentication is declared in the Agent Card. Inspect its security schemes and requirements; no credentials are supplied by the Catalog.";
  if (
    [
      "mcp-registry-server-1.8.1-f52dc85",
      "mcp-server-card-v1-experimental-526201b",
    ].includes(item.profileId) &&
    Array.isArray(data.remotes) &&
    data.remotes.some((remote) => object(remote).headers)
  )
    return "Connection headers are declared in the MCP document. Inspect required inputs; values and secrets are not copied into handoff references.";
  return "Authentication requirements are not summarized here. Inspect the retained representation before configuring a client; absence is not permission for anonymous access.";
}

/** Bounded consumer facts from recognized profiles; never infer execution or scan results. */
export function representationGuide(item: Representation): {
  title: string;
  guidance: string;
  facts: Reference[];
} {
  const facts: Reference[] = [];
  const add = (label: string, value: unknown) => {
    if (typeof value !== "string" || !value) return;
    facts.push({
      label,
      value:
        [...value].length <= 256
          ? value
          : "See complete representation metadata for the declaration.",
    });
  };
  const entries = (
    value: unknown,
    label: string,
    visit: (entry: Record<string, unknown>, index: number) => void,
  ) => {
    if (!Array.isArray(value)) return;
    value
      .slice(0, 16)
      .forEach((entry, index) => visit(object(entry), index + 1));
    if (value.length > 16)
      add(
        `${label} limit`,
        "First 16 shown; inspect the retained representation for the complete list.",
      );
  };
  const data = object(item.data);
  switch (item.profileId) {
    case "a2a-agent-card-1.0.1-3303592":
      add("Declared Agent version", data.version);
      return {
        title: "Agent interfaces",
        guidance:
          "Declared A2A interfaces are client connection metadata, not proof that an Agent is running. Check each interface's transport, protocol version and authentication requirements.",
        facts,
      };
    case "mcp-registry-server-1.8.1-f52dc85":
      add("Declared MCP server version", data.version);
      entries(data.packages, "Package", (entry, index) => {
        add(`Package ${index} registry`, entry.registryType);
        add(`Package ${index} identifier`, entry.identifier);
        add(`Package ${index} version`, entry.version);
        add(`Package ${index} transport`, object(entry.transport).type);
      });
      return {
        title: "MCP transports and packages",
        guidance:
          "This MCP Registry document can describe remote connections or packages. Package versions are separate from the server version. Inspect required inputs before configuring a client; no command is generated or executed.",
        facts,
      };
    case "mcp-server-card-v1-experimental-526201b": {
      add("Declared MCP server version", data.version);
      const remote =
        Array.isArray(data.remotes) &&
        data.remotes.some(
          (value) =>
            typeof object(value).url === "string" && !!object(value).url,
        );
      return {
        title: remote ? "MCP remote connections" : "MCP Server metadata",
        guidance: remote
          ? "This experimental MCP Server Card describes remote transports, not Registry packages or an inventory of runtime tools. Check declared protocol support and required inputs with the provider."
          : "This experimental MCP Server Card declares no remote connection. Check its retained metadata and provider documentation for connection requirements.",
        facts,
      };
    }
    case "agent-skills-69ef37e": {
      const inspection = object(item.package);
      if (
        inspection.structureState === "passed" ||
        inspection.structureState === "failed"
      )
        add("Package structure", inspection.structureState);
      if (inspection.structureState === "passed") {
        add("Skill root", inspection.rootDirectory);
        if (
          Number.isSafeInteger(inspection.fileCount) &&
          typeof inspection.fileCount === "number" &&
          inspection.fileCount >= 0
        )
          add("Inspected files", String(inspection.fileCount));
      }
      return {
        title: "Skill package and source",
        guidance:
          "A Skill document may omit supporting files. Use supplied Git/OCI pins or inspected package evidence to identify the complete tree. Structural inspection is not a malware, secrets or license assessment.",
        facts,
      };
    }
    case "agent-plugins-1.0.0-1fc1b627": {
      const inspection = object(item.plugin);
      add(
        "Plugin root",
        inspection.rootDirectory === "" ? "." : inspection.rootDirectory,
      );
      entries(inspection.components, "Component", (entry, index) => {
        if (entry.kind !== "skill" && entry.kind !== "mcp_server") return;
        const kind = entry.kind === "skill" ? "Skill" : "MCP Server";
        add(`${kind} component ${index}`, entry.name);
        add(`Component ${index} path`, entry.path);
      });
      return {
        title: "Plugin contents",
        guidance:
          "Listed Skill and MCP components come from retained archive inspection. Component documents are not the complete Plugin archive. No installation, component execution or security certification is implied.",
        facts,
      };
    }
    default:
      return {
        title: item.mediaType,
        guidance:
          "No kind-specific summary is available for this profile. Inspect the retained metadata; an unavailable summary is not a failed security check.",
        facts,
      };
  }
}
