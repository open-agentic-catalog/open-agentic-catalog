// Administrators select explicit host/audience participation, never client-supplied topology.
import { useEffect, useState } from "react";
import { result } from "../api/client";
import { permits } from "../api/permissions";
import { MetadataEditor } from "./MetadataEditor";
import { topologyShape } from "./federation-inputs";
import { Field, ReadState, useRead, type WorkspaceProps } from "./workspace";
import * as checks from "./responses";

type Props = WorkspaceProps & {
  value: string;
  onChange: (value: string) => void;
};
export function SourceTopology(props: Props) {
  return (
    <CurrentTopology
      key={JSON.stringify([props.access, props.id])}
      {...props}
    />
  );
}
function CurrentTopology(props: Props) {
  const [hosts, setHosts] = useState<{ host: string; mode: string }[]>([]);
  const canChoose = permits(props.access, "publication.read", "catalog.read");
  const [choices, setChoices] = useState(false);
  return (
    <details className="workspace-advanced">
      <summary>Advanced source participation</summary>
      <p>
        Indexed synchronization retains external metadata. Live participation
        can forward queries to approved sources. Discovery assignments select an
        exact configured host and audience; they do not activate a source or
        grant consent. Referrals and nesting remain explicit administrator
        policy.
      </p>
      <MetadataEditor
        id="source-topology"
        label="Topology"
        value={props.value}
        onChange={props.onChange}
        validate={topologyShape}
      >
        {(data, update) => (
          <>
            <h3>Discovery hosts and audiences</h3>
            <p>
              Use up to sixteen rows. Leave empty when this Source should not
              participate through a discovery host. Enter exact hosts manually;
              host inventory needs separate publication access. Edit referrals
              and nested manifests in JSON. Switching views preserves these
              settings.
            </p>
            {canChoose && (
              <button
                type="button"
                onClick={() => setChoices((v) => !v)}
                aria-expanded={choices}
              >
                {choices
                  ? "Hide configured host choices"
                  : "Load configured host choices"}
              </button>
            )}
            {choices && canChoose && (
              <HostInventory {...props} onData={setHosts} />
            )}
            {!data.discoveryAssignments?.length && (
              <p>No discovery audience assignments.</p>
            )}
            {(data.discoveryAssignments ?? []).map((assignment, index) => (
              <fieldset key={index}>
                <legend>Discovery assignment {index + 1}</legend>
                <Field label={`Discovery host ${index + 1}`}>
                  <input
                    required
                    maxLength={253}
                    value={assignment.host}
                    onChange={(event) =>
                      update({
                        ...data,
                        discoveryAssignments: data.discoveryAssignments!.map(
                          (item, i) =>
                            i === index
                              ? { ...item, host: event.target.value }
                              : item,
                        ),
                      })
                    }
                  />
                </Field>
                {choices && canChoose && (
                  <Field label={`Configured discovery host ${index + 1}`}>
                    <select
                      value=""
                      onChange={(event) => {
                        if (event.target.value)
                          update({
                            ...data,
                            discoveryAssignments:
                              data.discoveryAssignments!.map((item, i) =>
                                i === index
                                  ? { ...item, host: event.target.value }
                                  : item,
                              ),
                          });
                      }}
                    >
                      <option value="">Choose a configured host</option>
                      {hosts.map((item) => (
                        <option
                          key={item.host}
                          value={item.host}
                          disabled={item.mode === "disabled"}
                        >
                          {item.host} ({item.mode})
                        </option>
                      ))}
                    </select>
                  </Field>
                )}
                <Field label={`Discovery audience ${index + 1}`}>
                  <select
                    value={assignment.audience}
                    onChange={(event) =>
                      update({
                        ...data,
                        discoveryAssignments: data.discoveryAssignments!.map(
                          (item, i) =>
                            i === index
                              ? {
                                  ...item,
                                  audience: event.target.value as
                                    "anonymous" | "authenticated",
                                }
                              : item,
                        ),
                      })
                    }
                  >
                    <option value="anonymous">Anonymous</option>
                    <option value="authenticated">Authenticated</option>
                  </select>
                </Field>
                <button
                  type="button"
                  onClick={() => {
                    if (
                      assignment.host &&
                      !window.confirm("Remove this discovery assignment?")
                    )
                      return;
                    update({
                      ...data,
                      discoveryAssignments: data.discoveryAssignments!.filter(
                        (_, i) => i !== index,
                      ),
                    });
                  }}
                >
                  Remove discovery assignment {index + 1}
                </button>
              </fieldset>
            ))}
            <button
              type="button"
              disabled={(data.discoveryAssignments?.length ?? 0) >= 16}
              onClick={() =>
                update({
                  ...data,
                  discoveryAssignments: [
                    ...(data.discoveryAssignments ?? []),
                    { host: "", audience: "anonymous" },
                  ],
                })
              }
            >
              Add discovery assignment
            </button>
          </>
        )}
      </MetadataEditor>
    </details>
  );
}
function HostInventory(
  props: WorkspaceProps & {
    onData: (hosts: { host: string; mode: string }[]) => void;
  },
) {
  const page = useRead(
    "source-hosts",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/publication/hosts", { signal }),
        checks.PublicationHostInventory,
      ),
    props.onFailure,
  );
  const { onData } = props;
  useEffect(() => {
    onData(page.data?.items ?? []);
  }, [page.data, onData]);
  return (
    <>
      <ReadState {...page} />
      <p className="muted">
        Configuration only; this does not establish health or consent. Select
        each audience separately.
      </p>
    </>
  );
}
