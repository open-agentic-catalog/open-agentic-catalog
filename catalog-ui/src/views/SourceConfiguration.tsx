import { Facts } from "./Facts";
import type { components } from "../api/schema";
import { EvidenceDetails } from "./authoring";
import { ScrollableTable } from "./ScrollableTable";

type Schema = components["schemas"];
type Configuration = Partial<Schema["ConnectSource"]>;
const fields = [
  ["displayName", "Display name"],
  ["retrievalMode", "Search mode"],
  ["updateMode", "Synchronization"],
  ["scope", "Metadata to include"],
  ["artworkEnabled", "Source artwork"],
  ["trustVerificationPolicy", "ANS policy"],
  ["live", "Live query disclosure"],
  ["referrals", "Referral policy"],
  ["nesting", "Nested retrieval policy"],
  ["discoveryAssignments", "Discovery audiences"],
  ["networkProfile", "Network profile reference"],
] as const;

/** Use bounded decoded-data controls for full settings, keeping credential bindings private. */
export function SourceConfiguration({
  saved,
  proposed,
  remove = false,
}: {
  saved?: Schema["FederationSource"];
  proposed: Configuration;
  remove?: boolean;
}) {
  const defaults: Configuration = {
    retrievalMode: "indexed",
    updateMode: "manual_review",
    artworkEnabled: false,
    live: {},
    referrals: {},
    nesting: {},
    discoveryAssignments: [],
    trustVerificationPolicy: "",
  };
  const next = { ...defaults, ...saved, ...proposed };
  return (
    <section aria-label="Prepared source configuration">
      <h3>
        {remove
          ? "Configuration to remove"
          : "Saved and proposed configuration"}
      </h3>
      <p>
        Review the saved and proposed settings. Credentials are hidden.
        Discovery settings do not approve content.
      </p>
      <ScrollableTable label="Prepared source settings comparison">
        <table className="workspace-table">
          <thead>
            <tr>
              <th>Setting</th>
              {saved && <th>Saved</th>}
              {!remove && <th>Proposed</th>}
            </tr>
          </thead>
          <tbody>
            {fields.map(([key, label]) => (
              <tr key={key}>
                <th scope="row">{label}</th>
                {saved && (
                  <td>
                    <ConfigurationValue
                      label={`Saved ${label}`}
                      value={saved[key] ?? defaults[key]}
                    />
                  </td>
                )}
                {!remove && (
                  <td>
                    <ConfigurationValue
                      label={`Proposed ${label}`}
                      value={next[key]}
                    />
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollableTable>
      <Facts
        rows={[
          [
            "Network and credential changes",
            remove
              ? "Remove configuration; preserve retained evidence"
              : proposed.inspectionId
                ? `Use the captured inspection ${proposed.inspectionId} and its explicit network/credential bindings; empty replacement references clear them`
                : "Keep existing network and credential bindings",
          ],
          [
            "Credential binding",
            remove
              ? "Withheld"
              : proposed.inspectionId
                ? proposed.credentialBinding
                  ? "Configured deployment reference (value withheld)"
                  : "None"
                : saved?.credentialBinding
                  ? "Existing deployment reference (value withheld)"
                  : "None",
          ],
        ]}
      />
    </section>
  );
}

function ConfigurationValue({
  label,
  value,
}: {
  label: string;
  value: unknown;
}) {
  if (value === undefined) return <>Deployment default / not configured</>;
  if (typeof value === "string") return <>{value || "(empty)"}</>;
  if (typeof value === "boolean") return <>{value ? "Yes" : "No"}</>;
  return <EvidenceDetails title={label} value={value} />;
}
