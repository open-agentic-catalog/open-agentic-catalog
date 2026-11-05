import { result } from "../api/client";
import { Panel, Observation } from "./overview-presentation";
import { publicationOverview } from "./review-overview-values";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import { ScrollableTable } from "./ScrollableTable";

const mismatchLabels = {
  audience: "Audience differs",
  ard_profile: "ARD profile differs",
  ai_catalog_profile: "AI Catalog profile differs",
};
export default function PublicationOverview(props: WorkspaceProps) {
  const read = useRead(
    "publication-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/publication", { signal }),
        (value) => publicationOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel title="Discovery publication" wide>
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.hosts.length === 0 ? (
            <p>No publication hosts are configured for this tenant.</p>
          ) : (
            <ScrollableTable label="Publication hosts">
              <table>
                <caption>Publication hosts</caption>
                <thead>
                  <tr>
                    <th scope="col">Host</th>
                    <th scope="col">Audience</th>
                    <th scope="col">Active snapshot</th>
                    <th scope="col">Published on</th>
                    {read.data.hosts.some(
                      (host) => host.mismatches.length > 0,
                    ) && <th scope="col">Configuration changes</th>}
                  </tr>
                </thead>
                <tbody>
                  {read.data.hosts.map((host) => (
                    <tr key={host.host}>
                      <th scope="row">
                        {host.mode === "disabled" ? (
                          host.host
                        ) : (
                          <a
                            href={`${props.runtime.basePath}/publication?host=${encodeURIComponent(host.host)}&audience=${host.mode}`}
                          >
                            {host.host}
                          </a>
                        )}
                      </th>
                      <td>
                        {host.mode === "disabled"
                          ? "Disabled"
                          : host.mode === "anonymous"
                            ? "Public"
                            : "Authenticated"}
                      </td>
                      <td>
                        {host.active
                          ? `Generation ${host.active.generation}`
                          : host.status === "disabled"
                            ? "Disabled"
                            : "No active snapshot"}
                      </td>
                      <td>
                        {host.active?.activatedAt ? (
                          <time dateTime={host.active.activatedAt}>
                            {new Date(host.active.activatedAt).toLocaleString()}
                          </time>
                        ) : (
                          "-"
                        )}
                      </td>
                      {read.data!.hosts.some(
                        (item) => item.mismatches.length > 0,
                      ) && (
                        <td>
                          {host.mismatches.length
                            ? host.mismatches
                                .map((key) => mismatchLabels[key])
                                .join("; ")
                            : "-"}
                          {host.mismatches.includes("audience") &&
                            host.active && (
                              <div className="overview-secondary">
                                Published:{" "}
                                {host.active.audience === "anonymous"
                                  ? "Public"
                                  : "Authenticated"}
                              </div>
                            )}
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollableTable>
          )}
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}
