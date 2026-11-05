import { commandSummary } from "./workspace";
import { Fragment } from "react";
import type { components } from "../api/schema";
import type { Command } from "./workspace";
import { kindLabel } from "../resource-kinds";
import { catalogOrders } from "./catalog-order";
type Input = components["schemas"]["PersonalInput"];
type Reference = components["schemas"]["PersonalReference"];

/** Facts come from the saved body, never a target lookup or an inferred current release. */
export function referenceFacts(
  ref: Reference,
): NonNullable<Command["summary"]> {
  return ref.origin === "local"
    ? commandSummary([
        ["Origin", "Local release reference"] as const,
        ["Release ID", ref.releaseId!] as const,
        ["Version revision ID", ref.versionRevisionId!] as const,
      ])
    : commandSummary([
        ["Origin", "Exact foreign source reference"] as const,
        ["Kind", kindLabel(ref.kind!)] as const,
        ["Source ID", ref.sourceId!] as const,
        [
          "Presentation generation",
          String(ref.presentationGeneration),
        ] as const,
        ["Canonical identifier", ref.identifier!] as const,
        [
          "Version presence",
          ref.versionPresent ? "Present" : "Absent",
        ] as const,
        ...(ref.versionPresent
          ? [["Version", ref.version || "Empty version"] as const]
          : []),
      ]);
}
export function personalFacts(input: Input): NonNullable<Command["summary"]> {
  const facts = commandSummary([
    ["Saved label", input.label] as const,
    [
      "Saved type",
      input.type === "search" ? "Named search" : "Resource reference",
    ] as const,
  ]);
  if (input.reference) return [...facts, ...referenceFacts(input.reference)];
  const labels: Record<string, string> = {
    text: "Search text",
    order: "Sort by",
    origins: "Origins",
    namespaceId: "Namespace ID",
    kinds: "Resource kinds",
    tags: "Tags",
    categories: "Categories",
    license: "License declaration",
    publisher: "Publisher selector",
    visibility: "Visibility",
  };
  return [
    ...facts,
    ...Object.entries(input.search!).map(([key, value]) => ({
      label: labels[key]!,
      value:
        key === "order"
          ? catalogOrders.find((order) => order.value === value)!.label
          : Array.isArray(value)
            ? JSON.stringify(value)
            : value === ""
              ? "Empty value"
              : String(value),
    })),
    ...(input.search!.origins === undefined
      ? [{ label: "Origins", value: "Not specified" }]
      : []),
  ];
}
export function PersonalSummary({
  input,
  referenceOnly = false,
}: {
  input: Input;
  referenceOnly?: boolean;
}) {
  const facts = referenceOnly
    ? referenceFacts(input.reference!)
    : personalFacts(input).slice(2);
  return (
    <dl className="workspace-kv">
      {facts.map((fact, at) => (
        <Fragment key={`${fact.label}:${at}`}>
          <dt>{fact.label}</dt>
          <dd className="personal-value">{fact.value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}
