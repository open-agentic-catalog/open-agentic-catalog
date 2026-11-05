// Detail links use current target permissions independently from readable Catalog metadata.
import type { Access, Client, RecordDetail } from "../api/client";
import { permitsNamespace } from "../api/permissions";
import { useNamespaceAccessState } from "./namespace-access";

type Props = {
  access?: Access | undefined;
  client: Client;
  record: RecordDetail;
  basePath: string;
  compact: boolean;
  onFailure: (error: unknown) => void;
  state?: ReturnType<typeof useNamespaceAccessState> | undefined;
};

export function RecordWorkflows(props: Props) {
  return props.state ? (
    <WorkflowLinks {...props} state={props.state} />
  ) : props.access ? (
    <CurrentWorkflows {...props} access={props.access} />
  ) : (
    <p className="muted">Workflow permissions have not been confirmed.</p>
  );
}

function CurrentWorkflows(props: Props & { access: Access }) {
  const state = useNamespaceAccessState(
    props,
    props.record.resource.namespaceId,
  );
  return <WorkflowLinks {...props} state={state} />;
}

function WorkflowLinks(
  props: Props & { state: ReturnType<typeof useNamespaceAccessState> },
) {
  const { record, basePath, compact, state } = props;
  const permitted = (permission: Access["permissions"][number]) =>
    permitsNamespace(
      state.access,
      permission,
      "catalog.read",
      record.resource.namespaceId,
    );
  const metadata = permitted("resource.read");
  const content = permitted("resource.read_content");
  const history = permitted("resource.read_history");
  return (
    <section aria-label="Record workflow access">
      {state.busy && (
        <p role="status">Checking this namespace's workflow permissions...</p>
      )}
      {state.error && (
        <p className="notice">
          Workflow permissions could not be confirmed.{" "}
          <button onClick={state.refresh}>Retry namespace permissions</button>
        </p>
      )}
      {(content || (!compact && (metadata || history))) && (
        <nav
          className="record-links"
          aria-label={compact ? "Retained content" : "Record workflows"}
        >
          {content && (
            <a
              href={`${basePath}/version-revisions/${record.versionRevision.id}`}
            >
              Inspect retained version revision
            </a>
          )}
          {!compact && metadata && (
            <>
              <a href={`${basePath}/resources/${record.resource.id}`}>
                {history ? "Resource and versions" : "Resource metadata"}
              </a>
              <a href={`${basePath}/listings/${record.listing.id}`}>
                {history ? "Listing and revisions" : "Listing metadata"}
              </a>
            </>
          )}
          {!compact && history && (
            <>
              {metadata && (
                <a
                  href={`${basePath}/listings/${record.listing.id}#release-history`}
                >
                  Release history
                </a>
              )}
              <a href={`${basePath}/reviews/${record.release.submissionId}`}>
                Approved review
              </a>
              <a href={`${basePath}/releases/${record.release.id}`}>
                Release details
              </a>
            </>
          )}
        </nav>
      )}
      {!content && (
        <p className="muted">
          Retained revision access requires resource.read_content in this
          namespace and catalog.read scope when enforced.
        </p>
      )}
      {!compact && !history && (
        <p className="muted">
          Review and release history require resource.read_history in this
          namespace and catalog.read scope when enforced. These permissions are
          separate from readable Catalog metadata.
        </p>
      )}
    </section>
  );
}
