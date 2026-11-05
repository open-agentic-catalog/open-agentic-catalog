import { DraftFileActions } from "./DraftFileActions";
import {
  draftRepresentations,
  editorDraft,
  type ListingSnapshot,
} from "./draft-editor";
import { controlIcons } from "../shell/Icon";
// One screen owns the identity/content draft and each acknowledged command.
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result, type Kind } from "../api/client";
import {
  contributionReason,
  permitsNamespace,
  resourceCreationAccess,
} from "../api/permissions";
import { kindChoices, kindLabel } from "../resource-kinds";
import { KindBadge } from "./KindBadge";
import {
  ActionForm,
  EvidenceDetails,
  reportInvalidFields,
  type ActionValidation,
} from "./authoring";
import { NamespaceChoice } from "./NamespaceChoice";
import { ResourceIdentity } from "./ResourceIdentity";
import { canonicalResourceIdentifier } from "./resource-identity";
import { characterText } from "./input-validation";
import { object } from "../api/values";
import { useNamespaceAccessState } from "./namespace-access";
import { Field, ReadState, useUnsaved, type WorkspaceProps } from "./workspace";
import { NativeAuthoringHelp } from "./NativeAuthoringHelp";
import {
  RepresentationList,
  newRepresentation,
  representationInput,
} from "./representations";
import * as checks from "./responses";

type Resource = components["schemas"]["Resource"];
type Version = components["schemas"]["ResourceVersion"];
type Bundle = import("./authoring-save").VersionBundle;
const VersionSource = lazy(() => import("./VersionSource"));
const ListingPreparation = lazy(() => import("./ListingPreparation"));
import type { ListingPreparationHandle } from "./ListingPreparation";
import {
  commandETag,
  savePreparedListing,
  type ListingCheckpoint,
} from "./authoring-save";
import { newVersionContent } from "./new-version-content";
import { ValidationFindings } from "./ValidationFindings";

/** Entry for a new item, or a new Version of an already resolved Resource. */
export function AuthoringWorkspace(
  props: WorkspaceProps & {
    resource?: Resource;
    sourceRevision?: string | undefined;
  },
) {
  const [namespaceId, setNamespace] = useState(
    props.resource?.namespaceId ?? "",
  );
  const [cleanNamespace, setCleanNamespace] = useState(namespaceId);
  const [identifier, setIdentifier] = useState(
    props.resource?.canonicalIdentifier ?? "",
  );
  const [identityDraft, setIdentityDraft] = useState(false);
  const [kind, setKind] = useState<Kind>(props.resource?.kind ?? "agent");
  const [cleanKind, setCleanKind] = useState(kind);
  const [label, setLabel] = useState("");
  const [representations, setRepresentations] = useState(() => [
    newRepresentation(kind),
  ]);
  const [cleanRepresentations, setCleanRepresentations] = useState(() =>
    JSON.stringify(representations),
  );
  const [step, setStep] = useState<"basics" | "content" | "listing">(
    props.resource ? "content" : "basics",
  );
  const [activeResource, setResource] = useState(props.resource);
  const [lastSaved, setLastSaved] = useState<{
    resource: Resource;
    version?: Version;
    bundle?: Bundle;
    etag?: string;
  }>();
  const [correctionFocus, setCorrectionFocus] = useState<string[]>();
  const [commandSent, setCommandSent] = useState(false);
  const [listingEpoch, setListingEpoch] = useState(0);
  const [fileImported, setFileImported] = useState(false);
  const importNotice = useRef<HTMLParagraphElement>(null);
  const [listingSnapshot, setListingSnapshot] = useState<ListingSnapshot>();
  const [pending, setPending] = useState(false);
  const [source, setSource] = useState("");
  const [sourceOpen, setSourceOpen] = useState(!!props.sourceRevision);
  const [includeListing, setIncludeListing] = useState(false);
  const [correcting, setCorrecting] = useState(false);
  const [listingOnly, setListingOnly] = useState(false);
  const [listingSaved, setListingSaved] = useState<ListingCheckpoint>();
  const listingHandle = useRef<ListingPreparationHandle | undefined>(undefined);
  const [savedContent, setSavedContent] = useState(false);
  const [error, setError] = useState("");
  const [epoch, setEpoch] = useState(0);
  const firstContent = useRef<HTMLInputElement>(null);
  const errorRef = useRef<HTMLParagraphElement>(null);
  const basics = useRef<HTMLFieldSetElement>(null);
  const contentFields = useRef<HTMLFieldSetElement>(null);
  const validation = useRef<ActionValidation>(null);
  const [labelTouched, setLabelTouched] = useState(false);
  const touchLabel = () => setLabelTouched(true);
  const trimmedLabel = label.replace(
    /^\p{White_Space}+|\p{White_Space}+$/gu,
    "",
  );
  let labelError = "";
  try {
    if (!trimmedLabel) throw new Error("Enter a version label.");
    characterText(trimmedLabel, "Version label", 128, 1);
  } catch (cause) {
    labelError = (cause as Error).message;
  }
  useEffect(() => {
    firstContent.current?.setCustomValidity(labelError);
  }, [labelError]);
  useEffect(() => {
    if (fileImported) importNotice.current?.focus();
  }, [fileImported, epoch]);
  useEffect(() => {
    if (error) errorRef.current?.focus();
  }, [error]);
  const namespace = useNamespaceAccessState(props, namespaceId);
  const access = namespace.access;
  const createReason = activeResource
    ? undefined
    : resourceCreationAccess(props.access) === "denied"
      ? contributionReason(props.access, undefined, "resource.create")
      : contributionReason(access, namespaceId, "resource.create");
  const editReason = contributionReason(access, namespaceId);
  const canEdit = !namespace.busy && !namespace.error && !editReason;
  const editable = props.resource
    ? canEdit
    : resourceCreationAccess(props.access) !== "denied" ||
      (!!activeResource && canEdit);
  const contentSave = step !== "basics";
  const ready =
    !namespace.busy &&
    !namespace.error &&
    !createReason &&
    (!contentSave || !editReason);
  const allowed =
    ready &&
    (!activeResource || contentSave) &&
    (!includeListing || step === "listing");
  const representationsChanged =
    JSON.stringify(representations) !== cleanRepresentations;
  const contentChanged = !!label || representationsChanged;
  const changed =
    (!activeResource &&
      (namespaceId !== cleanNamespace ||
        kind !== cleanKind ||
        !!identifier ||
        identityDraft)) ||
    contentChanged;
  // A successful identity-only command does not save the content draft.
  useUnsaved(!!activeResource && !savedContent && contentChanged);
  const continueContent =
    !savedContent && !!activeResource && (canEdit || contentChanged);
  const validateIdentity = () => {
    if (!namespaceId)
      throw new Error("Choose a Namespace or enter its exact ID.");
    const supplied = characterText(
      identifier.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, ""),
      "Canonical identifier",
      512,
      1,
    );
    canonicalResourceIdentifier(supplied);
    return { namespaceId, canonicalIdentifier: supplied, kind };
  };
  const nativeName = () => {
    try {
      const item: unknown = JSON.parse(representations[0]?.content ?? "");
      return item &&
        typeof item === "object" &&
        "name" in item &&
        typeof item.name === "string"
        ? item.name
        : "";
    } catch {
      return "";
    }
  };
  const correctDraft = (focus = false) => {
    const row = representations[0];
    setCorrectionFocus(
      focus && row
        ? [`${row.editorID}-native-version`, `${row.editorID}-content`]
        : undefined,
    );
    setCorrecting(true);
    setListingOnly(false);
    setSavedContent(false);
    setStep("content");
    setEpoch((value) => value + 1);
  };
  const reset = () => {
    if (!savedContent && activeResource && canEdit) {
      setStep("content");
      setError("");
      queueMicrotask(() => firstContent.current?.focus());
      return;
    }
    setCommandSent(false);
    setListingSnapshot(undefined);
    setListingEpoch((value) => value + 1);
    setFileImported(false);
    setIdentityDraft(false);
    setCorrecting(false);
    setListingOnly(false);
    setIncludeListing(false);
    setSource("");
    setSourceOpen(false);
    setListingSaved(undefined);
    setResource(props.resource);
    setIdentifier(props.resource?.canonicalIdentifier ?? "");
    setNamespace(props.resource?.namespaceId ?? namespaceId);
    setCleanNamespace(props.resource?.namespaceId ?? namespaceId);
    setCleanKind(kind);
    setLabel("");
    setLabelTouched(false);
    const empty = [newRepresentation(kind)];
    setRepresentations(empty);
    setCleanRepresentations(JSON.stringify(empty));
    setSavedContent(false);
    setStep(props.resource ? "content" : "basics");
    setEpoch((value) => value + 1);
    setError("");
  };
  return (
    <section className="authoring-workspace">
      {!props.resource && <h2>Create {kindLabel(kind)}</h2>}
      {!props.resource && (
        <>
          <p>
            <KindBadge kind={kind} /> Save a draft now. Review and publication
            are separate actions.
          </p>
          <ol className="authoring-steps" aria-label="Authoring progress">
            <li aria-current={step === "basics" ? "step" : undefined}>
              Basics
            </li>
            <li aria-current={step === "content" ? "step" : undefined}>
              Content
            </li>
            {includeListing && (
              <li aria-current={step === "listing" ? "step" : undefined}>
                Listing details
              </li>
            )}
            <li>Check and save draft</li>
          </ol>
        </>
      )}
      <ReadState {...namespace} />
      <div className="authoring-file-toolbar">
        {props.resource &&
          permitsNamespace(
            access,
            "resource.read",
            "catalog.read",
            namespaceId,
          ) && (
            <a
              href={`${props.runtime.basePath}/resources/${props.resource.id}`}
            >
              Back to resource
            </a>
          )}
        <DraftFileActions
          documentationUrl={props.runtime.documentationUrl}
          key={`${epoch}:${props.resource?.id}:${namespaceId}:${kind}:${identifier}:${JSON.stringify(access)}`}
          locked={
            !editable ||
            namespace.busy ||
            !!namespace.error ||
            pending ||
            commandSent ||
            correcting ||
            listingOnly ||
            savedContent ||
            (!props.resource && !!activeResource)
          }
          downloadLocked={
            !editable || namespace.busy || !!namespace.error || pending
          }
          dirty={changed || includeListing}
          target={props.resource}
          read={() => {
            if (identityDraft)
              throw new Error(
                "Apply or clear the AIR builder fields before downloading draft JSON.",
              );
            if (includeListing && !listingHandle.current)
              throw new Error(
                "Wait for the complete Listing lookup before downloading draft JSON.",
              );
            return editorDraft(
              kind,
              identifier,
              label,
              representations,
              includeListing ? listingHandle.current!.snapshot() : undefined,
            );
          }}
          apply={(file, choice) => {
            const rows = draftRepresentations(file);
            setIdentifier(
              props.resource?.canonicalIdentifier ?? file.canonicalIdentifier,
            );
            setKind(props.resource?.kind ?? file.kind);
            setLabel(file.version);
            setLabelTouched(false);
            setRepresentations(rows);
            setSource("");
            setSourceOpen(false);
            setIdentityDraft(false);
            setIncludeListing(choice !== "none");
            setListingSnapshot(
              choice === "imported" ? structuredClone(file.listing) : undefined,
            );
            setListingSaved(undefined);
            setStep(props.resource ? "content" : "basics");
            setFileImported(true);
            setListingEpoch((value) => value + 1);
            setError("");
            setEpoch((value) => value + 1);
          }}
        />
      </div>
      {fileImported && (
        <p role="status" tabIndex={-1} ref={importNotice}>
          Draft imported into the editor. Check its fields before saving.
        </p>
      )}
      {error && (
        <p role="alert" tabIndex={-1} ref={errorRef}>
          {error}
        </p>
      )}
      <ActionForm
        requiredMarkers
        validation={validation}
        resetToken={epoch}
        focusAfterReset={correctionFocus}
        onPendingChange={setPending}
        title={
          listingOnly
            ? "Save Listing draft"
            : correcting
              ? "Save corrected draft"
              : contentSave
                ? props.resource
                  ? "Create version"
                  : "Save content draft"
                : "Create resource"
        }
        icon={contentSave ? "save" : "add"}
        intent={contentSave ? "primary" : "secondary"}
        description={
          listingOnly
            ? "Save Listing metadata for the exact content revision shown below."
            : contentSave
              ? "Check the complete draft before saving. The Catalog validates its content when you confirm."
              : "Choose Next: Content to add a version before saving. Create resource saves only its permanent identity."
        }
        editable={editable}
        allowed={allowed}
        unavailableReason={
          includeListing && step !== "listing"
            ? "Choose Next: Listing details before saving the optional Listing."
            : (createReason ??
              (contentSave || continueContent ? editReason : undefined))
        }
        dirty={changed}
        onFailure={props.onFailure}
        another={{
          allowed:
            !namespace.busy &&
            !namespace.error &&
            (continueContent
              ? canEdit
              : props.resource
                ? canEdit
                : permitsNamespace(
                    access,
                    "resource.create",
                    "catalog.write",
                    namespaceId,
                  )),
          label: continueContent
            ? "Continue to content"
            : props.resource
              ? "Create another version"
              : "Add another item",
          reset,
        }}
        prepare={() => {
          const body = validateIdentity();
          const expectedIdentifier = canonicalResourceIdentifier(
            body.canonicalIdentifier,
          );
          const tenantId = props.access.tenantId;
          const versionLabel = characterText(
            label.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, ""),
            "Version label",
            128,
            contentSave ? 1 : 0,
          );
          const native =
            source && !correcting && !listingOnly
              ? newVersionContent(kind, representations, versionLabel)
              : { drafts: representations, changes: [] };
          const versionBody =
            contentSave && !listingOnly
              ? {
                  version: characterText(
                    label.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, ""),
                    "Version label",
                    128,
                    1,
                  ),
                  representations: native.drafts.map(representationInput),
                }
              : undefined;
          if (
            versionBody &&
            versionBody.representations.filter(
              (item) => item.role === "primary",
            ).length !== 1
          )
            throw new Error("Include exactly one primary representation.");
          if (includeListing && !listingHandle.current)
            throw new Error(
              "Wait for the Listing lookup to complete. A failed lookup does not mean that no Listing exists.",
            );
          const listingPlan = includeListing
            ? listingHandle.current!.read()
            : undefined;
          const listingKeys = {
            listing: crypto.randomUUID(),
            revision: crypto.randomUUID(),
          };
          const checkpoint: ListingCheckpoint = {
            listing: listingSaved?.listing ?? listingPlan?.listing,
            etag: listingSaved?.etag ?? listingPlan?.etag,
          };
          const correction = correcting ? lastSaved : undefined;
          let saved =
            listingOnly || (savedContent && !correcting)
              ? lastSaved?.bundle
              : undefined;
          const versionKey = crypto.randomUUID();
          let acknowledged = activeResource;
          return {
            summary: [
              { label: "Canonical identifier", value: expectedIdentifier },
              { label: "Namespace ID", value: body.namespaceId },
              { label: "Kind", value: body.kind },
              {
                label: "Save scope",
                value: versionBody
                  ? `${acknowledged ? "Existing" : "New"} Resource and ${correction ? "new VersionRevision" : "new Version"}${listingPlan ? "; save Listing and ListingRevision" : ""}`
                  : listingOnly
                    ? "Existing content; save Listing and ListingRevision"
                    : "Resource identity only",
              },
              ...native.changes.map((value) => ({
                label: "Native version update",
                value,
              })),
              ...(source ? [{ label: "Source revision", value: source }] : []),
              ...(listingPlan
                ? [
                    {
                      label: "Listing",
                      value:
                        checkpoint.listing?.id ?? "Create for this Resource",
                    },
                    {
                      label: "Listing name",
                      value:
                        listingPlan.body.displayName ||
                        "Use validated primary name",
                    },
                    {
                      label: "Listing version link",
                      value:
                        saved?.revision.id ??
                        "Exact revision acknowledged by this save",
                    },
                    { label: "Publisher", value: listingPlan.body.publisherId },
                    { label: "Visibility", value: listingPlan.body.visibility },
                  ]
                : []),
              ...(versionBody
                ? [
                    { label: "Version", value: versionBody.version },
                    {
                      label: "Representations",
                      value: String(versionBody.representations.length),
                    },
                  ]
                : []),
            ],
            run: async (key: string) => {
              setCommandSent(true);
              setFileImported(false);
              if (!acknowledged) {
                acknowledged = result(
                  await props.client.api.POST("/api/v1/resources", {
                    params: { header: { "Idempotency-Key": key } },
                    body,
                  }),
                  (value) =>
                    checks.Resource(value) &&
                    object(value) &&
                    value.tenantId === tenantId &&
                    value.namespaceId === body.namespaceId &&
                    value.kind === body.kind &&
                    value.canonicalIdentifier === expectedIdentifier,
                );
                setResource(acknowledged);
                setIdentifier(acknowledged.canonicalIdentifier);
                setLastSaved({ resource: acknowledged });
              }
              if (!versionBody && !saved) {
                setSavedContent(false);
                return "Resource created.";
              }
              const resource = acknowledged;
              if (!saved && versionBody) {
                const reply = correction?.version
                  ? await props.client.api.POST(
                      "/api/v1/resource-versions/{versionId}/revisions",
                      {
                        params: {
                          path: { versionId: correction.version.id },
                          header: {
                            "Idempotency-Key": versionKey,
                            "If-Match": correction.etag!,
                          },
                        },
                        body: { representations: versionBody.representations },
                      },
                    )
                  : await props.client.api.POST(
                      "/api/v1/resources/{resourceId}/versions",
                      {
                        params: {
                          path: { resourceId: resource.id },
                          header: { "Idempotency-Key": versionKey },
                        },
                        body: versionBody,
                      },
                    );
                result(reply);
                const etag = commandETag(reply.response);
                saved = result(reply, (value) =>
                  checks.versionCommandResult(value, {
                    tenantId,
                    resourceId: resource.id,
                    label: versionBody.version,
                    ...(correction?.version
                      ? { versionId: correction.version.id }
                      : {}),
                  }),
                );
                setLastSaved({
                  resource,
                  version: saved.version,
                  bundle: saved,
                  etag,
                });
                if (native.changes.length) setRepresentations(native.drafts);
              }
              const bundle = saved!;
              setSavedContent(true);
              if (listingPlan) {
                const outcome = await savePreparedListing(
                  props.client,
                  resource,
                  bundle,
                  listingPlan,
                  checkpoint,
                  listingKeys,
                  setListingSaved,
                );
                setListingSaved({ ...outcome });
                return `Draft and Listing saved. Version ${bundle.version.version}. ${bundle.evidence.state === "complete" ? "Open the Listing for review and publication options." : "Content corrections are still needed."}`;
              }
              return bundle.evidence.state === "complete"
                ? `Draft saved. Version ${bundle.version.version}.`
                : `Draft saved, corrections needed. See the exact findings below.`;
            },
          };
        }}
      >
        {!props.resource && (
          <fieldset
            ref={basics}
            tabIndex={-1}
            disabled={step !== "basics" || !!activeResource}
            hidden={step !== "basics"}
          >
            <legend>Basics</legend>
            <NamespaceChoice
              preserveDraft
              workspace={props}
              value={namespaceId}
              onChange={setNamespace}
            />
            <ResourceIdentity
              key={epoch}
              value={identifier}
              onChange={setIdentifier}
              itemName={nativeName()}
              onDraftChange={setIdentityDraft}
            />
            <Field label="Kind">
              <select
                required
                value={kind}
                onChange={(event) => {
                  const next = event.target.value as Kind;
                  if (next === kind) return;
                  if (
                    representationsChanged &&
                    !window.confirm(
                      "Changing the kind replaces the entered content and staged files. Continue?",
                    )
                  )
                    return;
                  setKind(next);
                  const empty = [newRepresentation(next)];
                  setRepresentations(empty);
                  setCleanRepresentations(JSON.stringify(empty));
                }}
              >
                {kindChoices.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.singular}
                  </option>
                ))}
              </select>
            </Field>
          </fieldset>
        )}
        {activeResource && (
          <p>
            {props.resource && <KindBadge kind={kind} />} Resource:{" "}
            <code>{activeResource.canonicalIdentifier}</code>. Its Namespace,
            kind and identifier are fixed.
            {source && (
              <span aria-label="Selected source">
                {" "}
                Source revision: <code>{source}</code>.
              </span>
            )}
          </p>
        )}
        <fieldset
          ref={contentFields}
          hidden={step !== "content"}
          disabled={
            step !== "content" ||
            !canEdit ||
            listingOnly ||
            (savedContent && !correcting)
          }
        >
          <legend className="sr-only">Content</legend>
          <NativeAuthoringHelp
            kind={kind}
            documentationUrl={props.runtime.documentationUrl}
          />
          <Field
            label="Version label"
            error={labelTouched ? labelError : ""}
            hint="Use up to 128 characters. Match any version declared in the supplied document."
          >
            <input
              ref={firstContent}
              id="authoring-version-label"
              required
              readOnly={correcting || listingOnly}
              maxLength={256}
              value={label}
              onBlur={touchLabel}
              onInvalid={touchLabel}
              onFocus={label ? touchLabel : undefined}
              onChange={(event) => {
                touchLabel();
                setLabel(event.target.value);
              }}
            />
          </Field>
          <RepresentationList
            kind={kind}
            value={representations}
            onChange={setRepresentations}
            version={label}
            onVersionChange={setLabel}
          />
          {props.resource && !correcting && !listingOnly && (
            <details
              open={sourceOpen}
              onToggle={(event) => setSourceOpen(event.currentTarget.open)}
            >
              <summary>Start from an existing revision</summary>
              <Suspense
                fallback={<p role="status">Loading source choices...</p>}
              >
                <VersionSource
                  key={epoch}
                  {...props}
                  access={access}
                  resource={props.resource}
                  initialRevision={
                    epoch === 0 ? props.sourceRevision : undefined
                  }
                  drafts={representations}
                  onApply={(drafts, revision) => {
                    setRepresentations(drafts);
                    setSource(revision);
                    setSourceOpen(false);
                  }}
                />
              </Suspense>
            </details>
          )}
        </fieldset>
        {contentSave && (
          <Field label="Include Listing details">
            <input
              type="checkbox"
              checked={includeListing}
              onChange={(event) => {
                setIncludeListing(event.target.checked);
                if (!event.target.checked) setStep("content");
              }}
            />
          </Field>
        )}
        {includeListing && (
          <fieldset hidden={step !== "listing"} disabled={step !== "listing"}>
            <legend className="sr-only">Listing</legend>
            <Suspense
              fallback={<p role="status">Loading Listing details...</p>}
            >
              <ListingPreparation
                key={listingEpoch}
                snapshot={listingSnapshot}
                {...props}
                access={access}
                namespaceId={namespaceId}
                resource={props.resource}
                known={
                  lastSaved?.resource.id === activeResource?.id
                    ? lastSaved?.bundle
                    : undefined
                }
                handle={listingHandle}
              />
            </Suspense>
          </fieldset>
        )}
        {step === "content" && includeListing && (
          <button
            type="button"
            onClick={() => {
              if (
                contentFields.current &&
                !reportInvalidFields(contentFields.current)
              )
                return;
              if (
                !validation.current?.check(() => {
                  representations.forEach(representationInput);
                })
              )
                return;
              setStep("listing");
            }}
          >
            {controlIcons.arrow} Next: Listing details
          </button>
        )}
        {step === "listing" && (
          <button
            type="button"
            onClick={() => {
              setStep("content");
              queueMicrotask(() => firstContent.current?.focus());
            }}
          >
            <span className="icon-back">{controlIcons.arrow}</span> Back:
            Content
          </button>
        )}
        {((step === "basics" && canEdit) ||
          (step === "content" && !props.resource)) && (
          <div className="form-actions">
            {step === "basics" && canEdit && (
              <button
                type="button"
                className="primary"
                disabled={!ready}
                onClick={() => {
                  try {
                    if (basics.current && !reportInvalidFields(basics.current))
                      return;
                    validateIdentity();
                    setError("");
                    setStep("content");
                    queueMicrotask(() => firstContent.current?.focus());
                  } catch (cause) {
                    setError(
                      cause instanceof Error
                        ? cause.message
                        : "Check the Basics fields.",
                    );
                  }
                }}
              >
                {controlIcons.arrow} Next: Content
              </button>
            )}
            {step === "content" && !props.resource && (
              <button
                type="button"
                onClick={() => {
                  setStep("basics");
                  queueMicrotask(() => {
                    const input =
                      basics.current?.querySelector<HTMLInputElement>(
                        "input:not(:disabled)",
                      );
                    if (input) input.focus();
                    else basics.current?.focus();
                  });
                }}
              >
                <span className="icon-back">{controlIcons.arrow}</span> Back:
                Basics
              </button>
            )}
          </div>
        )}
      </ActionForm>
      {lastSaved?.bundle && lastSaved.resource.id === activeResource?.id && (
        <section
          className="workspace-panel"
          aria-label="Saved draft validation"
        >
          <h2>
            {lastSaved.bundle.evidence.state === "complete"
              ? "Draft saved"
              : "Draft saved, corrections needed"}
          </h2>
          <ValidationFindings
            evidence={lastSaved.bundle.evidence}
            revisionId={lastSaved.bundle.revision.id}
            fieldLinks={
              canEdit &&
              !pending &&
              representations.length === 1 &&
              representations[0]?.mode === "data" &&
              [
                "application/a2a-agent-card+json",
                "application/mcp-server-card+json",
                "application/vnd.mcp.server+json",
              ].includes(representations[0].mediaType)
                ? {
                    "/representations/0/version": {
                      label: "Correct native version",
                      activate: () => correctDraft(true),
                    },
                  }
                : undefined
            }
          />
          <EvidenceDetails
            title="Saved representations"
            value={lastSaved.bundle.revision}
          />
          {canEdit && !lastSaved.version?.sealedRevisionId && (
            <button
              type="button"
              disabled={pending}
              onClick={() => {
                correctDraft();
              }}
            >
              Correct this draft
            </button>
          )}
          {canEdit && (
            <button
              type="button"
              disabled={pending}
              onClick={() => {
                setListingOnly(true);
                setCorrecting(false);
                setIncludeListing(true);
                setStep("listing");
                setEpoch((v) => v + 1);
              }}
            >
              Prepare Listing for saved content
            </button>
          )}
          <p className="muted">
            Correction creates a new immutable revision of an unsealed Version.
            Revalidation reruns checks on an existing revision. Sealed content
            needs a new Version. Resolve a pending review before submitting
            again.
          </p>
        </section>
      )}
      {listingSaved?.listing && (
        <section className="workspace-panel">
          <h2>Listing draft saved</h2>
          <p>
            Review, release publication and discovery snapshots are separate
            actions with separate permissions.
          </p>
          <a
            href={`${props.runtime.basePath}/listings/${listingSaved.listing.id}`}
          >
            Open saved Listing
          </a>
          <EvidenceDetails
            title="Saved Listing references"
            value={listingSaved}
          />
        </section>
      )}
      {lastSaved && (
        <>
          <EvidenceDetails
            title="Created resource references"
            value={{
              id: lastSaved.resource.id,
              namespaceId: lastSaved.resource.namespaceId,
              ...(lastSaved.version ? { versionId: lastSaved.version.id } : {}),
            }}
          />
          {permitsNamespace(
            lastSaved.resource.namespaceId === namespaceId
              ? access
              : props.access,
            "resource.read",
            "catalog.read",
            lastSaved.resource.namespaceId,
          ) && (
            <a
              className="button primary"
              href={`${props.runtime.basePath}/${lastSaved.version ? `versions/${lastSaved.version.id}` : `resources/${lastSaved.resource.id}`}`}
            >
              {lastSaved.version
                ? "Open saved version"
                : "Open created resource"}
            </a>
          )}
        </>
      )}
    </section>
  );
}
