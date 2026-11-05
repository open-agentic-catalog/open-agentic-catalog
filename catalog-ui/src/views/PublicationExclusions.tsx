import type { components } from "../api/schema";
import { permits } from "../api/permissions";
import type { WorkspaceProps } from "./workspace";
const explanations = new Map(
  Object.entries({
    LISTING_NOT_PUBLIC:
      "This anonymous snapshot requires public listing visibility.",
    LISTING_NOT_TENANT_VISIBLE:
      "This authenticated snapshot requires tenant or public visibility.",
    AIR_IDENTIFIER_REQUIRED:
      "The resource needs a valid canonical AIR identifier.",
    PUBLICATION_EVIDENCE_BLOCKED:
      "Validation evidence is missing, blocked, expired or does not match this revision. Inspect its findings and obtain current evidence. To change approved content, create a new version label. Create a listing revision that references its new version revision, then submit the listing for review.",
    STANDARD_PUBLIC_REPRESENTATION_REQUIRED:
      "A valid supported primary representation is required for this kind.",
    PUBLIC_DELIVERY_FORM_REQUIRED:
      "The primary representation needs an inline document or a supported deliverable reference.",
    REPRESENTATIVE_QUERIES_REQUIRED:
      "This ARD profile requires valid representative queries. Add queries to a new listing revision and follow review and release.",
  }),
);
export function PublicationExclusion({
  item,
  props,
}: {
  item: components["schemas"]["PublicationSnapshot"]["exclusions"][number];
  props: WorkspaceProps;
}) {
  return (
    <section>
      <h4>{item.identifier}</h4>
      <p>Profiles: {item.profiles.join(", ")}</p>
      <ul>
        {item.reasons.map((reason, index) => (
          <li key={index}>
            <code>{reason}</code>:{" "}
            {explanations.get(reason) ??
              "No local explanation is available for this code. Inspect the retained snapshot evidence."}
          </li>
        ))}
      </ul>
      {permits(props.access, "resource.read", "catalog.read") && (
        <a href={`${props.runtime.basePath}/resources/${item.resourceId}`}>
          Open excluded resource
        </a>
      )}
    </section>
  );
}
