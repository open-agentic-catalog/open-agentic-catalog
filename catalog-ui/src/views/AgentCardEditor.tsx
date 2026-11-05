import { useEffect, useRef, useState } from "react";
import { Field } from "./workspace";
import { NativeChoice } from "./NativeChoice";
import { controlIcons } from "../shell/Icon";
import { suggestResourceName } from "./resource-identity";
import type { RepresentationDraft } from "./representations";
import {
  agentFieldError,
  agentVersion,
  newAgentCard,
  patchAgentCard,
  readAgentCard,
  signedAgentCard,
  type AgentCard,
} from "./agent-card";

/** Keep native validity and visible errors on the same labelled control. */
function AgentField({
  id,
  label,
  value,
  onChange,
  type = "text",
  multiline = false,
  hint,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: "text" | "url" | "protocol" | "media";
  multiline?: boolean;
  hint?: string;
}) {
  const input = useRef<HTMLInputElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const [touched, setTouched] = useState(false);
  const touch = () => setTouched(true);
  const error = agentFieldError(value, type);
  const control = multiline ? textarea : input;
  useEffect(() => {
    control.current!.setCustomValidity(error);
  }, [control, error]);
  const common = {
    id,
    required: true,
    value,
    maxLength: 2 * 1024 * 1024,
    "aria-invalid": (touched && !!error) || undefined,
    "aria-describedby": touched && error ? `${id}-error` : undefined,
    onBlur: touch,
    onInvalid: touch,
    onFocus: value ? touch : undefined,
    onChange: (
      event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
    ) => {
      touch();
      onChange(event.target.value);
    },
  };
  return (
    <div>
      <Field
        label={label}
        error={touched ? error : ""}
        {...(hint ? { hint } : {})}
      >
        {multiline ? (
          <textarea ref={textarea} rows={3} {...common} />
        ) : (
          <input
            ref={input}
            type={type === "url" ? "url" : "text"}
            {...common}
          />
        )}
      </Field>
    </div>
  );
}

/** Preserve literal tag values, array order and a useful focus after row changes. */
function AgentValues({
  id,
  label,
  value,
  onChange,
  media = false,
}: {
  id: string;
  label: string;
  value: string[];
  onChange: (value: string[]) => void;
  media?: boolean;
}) {
  const container = useRef<HTMLFieldSetElement>(null);
  const focus = useRef<number | undefined>(undefined);
  const rows = value.length ? value : [""];
  useEffect(() => {
    if (focus.current === undefined) return;
    container
      .current!.querySelectorAll<HTMLElement>(".agent-value-row")
      .item(focus.current)
      .querySelector<HTMLElement>("input,select")!
      .focus();
    focus.current = undefined;
  }, [value]);
  const change = (index: number, next: string) =>
    onChange(rows.map((old, at) => (at === index ? next : old)));
  return (
    <fieldset ref={container} className="workspace-panel workspace-fields">
      <legend>{label}</legend>
      {media && <p>Choose Text or JSON, or enter a custom media type.</p>}
      {rows.map((item, index) => (
        <div key={index} className="agent-value-row">
          {media && (
            <Field label={`${label} ${index + 1}`}>
              <select
                value={
                  item === "text/plain" || item === "application/json"
                    ? item
                    : "custom"
                }
                onChange={(event) =>
                  change(
                    index,
                    event.target.value === "custom" ? "" : event.target.value,
                  )
                }
              >
                <option value="text/plain">Text</option>
                <option value="application/json">JSON</option>
                <option value="custom">Custom media type</option>
              </select>
            </Field>
          )}
          {(!media ||
            (item !== "text/plain" && item !== "application/json")) && (
            <AgentField
              id={`${id}-${index}`}
              label={
                media
                  ? `Custom ${label.toLowerCase()} ${index + 1}`
                  : `${label} ${index + 1}`
              }
              value={item}
              type={media ? "media" : "text"}
              onChange={(next) => change(index, next)}
            />
          )}
          {rows.length > 1 && (
            <button
              type="button"
              onClick={() => {
                focus.current = Math.min(index, rows.length - 2);
                onChange(rows.filter((_, at) => at !== index));
              }}
            >
              {controlIcons.remove} Remove {label.toLowerCase()} {index + 1}
            </button>
          )}
        </div>
      ))}
      <button
        type="button"
        onClick={() => {
          focus.current = rows.length;
          onChange([...rows, media ? "text/plain" : ""]);
        }}
      >
        {controlIcons.add} Add {label.toLowerCase()}
      </button>
    </fieldset>
  );
}

/** Only explicit guided changes patch a Card. Viewing and importing leave its text intact. */
export default function AgentCardEditor({
  value,
  onChange,
  version = "",
  advanced,
  onAdvanced,
  onSignedChange,
  origin,
}: {
  value: RepresentationDraft;
  onChange: (value: RepresentationDraft) => void;
  version?: string | undefined;
  advanced: boolean;
  onAdvanced: () => void;
  onSignedChange: (signed: boolean) => void;
  origin: number;
}) {
  const parsed = readAgentCard(value.content, version);
  const card = parsed.card;
  const original = card ?? parsed.original;
  const signed = !!original && signedAgentCard(original);
  const lastGenerated = useRef<string | undefined>(undefined);
  const linkedOrigin = useRef(origin);
  const rowFocus = useRef<string | undefined>(undefined);
  const interfaceRows = useRef({ origin, keys: [] as number[], next: 0 });
  const [linked, setLinked] = useState(false);
  const current = useRef({
    value,
    onChange,
    onAdvanced,
    onSignedChange,
    origin,
  });
  current.current = { value, onChange, onAdvanced, onSignedChange, origin };
  useEffect(() => current.current.onSignedChange(signed), [signed]);
  const commit = (next: string, followVersion: boolean) => {
    lastGenerated.current = next;
    linkedOrigin.current = origin;
    setLinked(followVersion);
    onChange({ ...value, content: next });
  };
  const edit = (patch: Partial<AgentCard>, focusID?: string) => {
    if (!card || signed) return;
    rowFocus.current = focusID;
    const followVersion =
      !value.content ||
      (linked &&
        lastGenerated.current === value.content &&
        linkedOrigin.current === origin);
    commit(
      patchAgentCard(card, {
        ...patch,
        ...(followVersion ? { version: agentVersion(version) } : {}),
      }),
      followVersion,
    );
  };
  useEffect(() => {
    const target = rowFocus.current;
    if (target === undefined) return;
    rowFocus.current = undefined;
    document.getElementById(target)?.focus();
  }, [value]);
  useEffect(() => {
    const { value: latest, onChange: change } = current.current;
    if (
      !linked ||
      lastGenerated.current !== latest.content ||
      linkedOrigin.current !== current.current.origin
    )
      return;
    const read = readAgentCard(latest.content, version).card;
    if (!read || read.version === agentVersion(version)) return;
    const next = patchAgentCard(read, { version: agentVersion(version) });
    lastGenerated.current = next;
    change({ ...latest, content: next });
  }, [linked, version, origin]);
  useEffect(() => {
    if (!card) current.current.onAdvanced();
  }, [card]);
  const interfaces = card?.supportedInterfaces?.length
    ? card.supportedInterfaces
    : [{}];
  const abilities = card?.skills?.length ? card.skills : [{}];
  if (interfaceRows.current.origin !== origin) {
    interfaceRows.current = {
      origin,
      keys: [],
      next: interfaceRows.current.next,
    };
  }
  while (interfaceRows.current.keys.length < interfaces.length)
    interfaceRows.current.keys.push(interfaceRows.current.next++);
  interfaceRows.current.keys.length = interfaces.length;
  const moveInterface = (index: number, target: number) => {
    const next = [...interfaces];
    const [item] = next.splice(index, 1);
    next.splice(target, 0, item!);
    const [key] = interfaceRows.current.keys.splice(index, 1);
    interfaceRows.current.keys.splice(target, 0, key!);
    edit(
      { supportedInterfaces: next },
      `${value.editorID}-agent-url-${target}`,
    );
  };
  return (
    <div className="agent-card-editor workspace-fields">
      {!card && <p>{parsed.reason} Your document stays in Advanced JSON.</p>}
      {signed && (
        <div className="notice warning">
          <p>
            This Card contains signatures. Import an externally re-signed Card,
            or create an unsigned copy before editing. Signed JSON stays
            read-only.
          </p>
          <button
            type="button"
            onClick={() => {
              if (
                !window.confirm(
                  "Remove all Card signatures and continue with an unsigned copy? Other Card fields stay unchanged.",
                )
              )
                return;
              const next = { ...original };
              delete next.signatures;
              commit(JSON.stringify(next, null, 2) + "\n", false);
            }}
          >
            {controlIcons.copy} Create unsigned copy
          </button>
        </div>
      )}
      {card && (
        <fieldset
          hidden={advanced}
          disabled={advanced || signed}
          className="workspace-fields agent-guided-fields"
        >
          <legend className="sr-only">Agent Card fields</legend>
          <AgentField
            id={`${value.editorID}-agent-name`}
            label="Agent name"
            value={card.name ?? ""}
            hint="The name readers see in the Agent Card."
            onChange={(name) => edit({ name })}
          />
          <AgentField
            id={`${value.editorID}-agent-description`}
            label="Agent description"
            value={card.description ?? ""}
            multiline
            hint="Describe the Agent's purpose and the tasks it handles."
            onChange={(description) => edit({ description })}
          />
          <p>
            Card version:{" "}
            {value.content
              ? card.version || "Not set"
              : agentVersion(version) || "Enter the Version label above"}
            . A2A protocol versions are separate.
          </p>
          {value.content && card.version !== agentVersion(version) && (
            <button
              type="button"
              id={`${value.editorID}-native-version`}
              disabled={!agentVersion(version)}
              onClick={() => {
                commit(
                  patchAgentCard(card, { version: agentVersion(version) }),
                  true,
                );
              }}
            >
              {controlIcons.edit} Use Version label in this Card
            </button>
          )}
          <fieldset className="workspace-panel workspace-fields">
            <legend>Agent interfaces</legend>
            <p>
              The first interface is preferred. Enter an endpoint that this
              Agent supports.
            </p>
            {interfaces.map((item, index) => (
              <fieldset
                key={interfaceRows.current.keys[index]}
                className="workspace-panel workspace-fields"
              >
                <legend>
                  Interface {index + 1}
                  {index === 0 && (
                    <span className="preferred-interface">Preferred</span>
                  )}
                </legend>
                <AgentField
                  id={`${value.editorID}-agent-url-${index}`}
                  label={`Interface ${index + 1} HTTPS URL`}
                  value={item.url ?? ""}
                  type="url"
                  hint="Use the HTTPS address where clients connect to the Agent, not the Catalog address."
                  onChange={(url) =>
                    edit({
                      supportedInterfaces: interfaces.map((old, at) =>
                        at === index ? { ...old, url } : old,
                      ),
                    })
                  }
                />
                <details>
                  <summary>Interface {index + 1} protocol settings</summary>
                  <NativeChoice
                    id={`${value.editorID}-agent-binding-${index}`}
                    label={`Interface ${index + 1} protocol binding`}
                    customLabel={`Interface ${index + 1} custom protocol binding`}
                    choices={[
                      ["JSONRPC", "JSON-RPC"],
                      ["GRPC", "gRPC"],
                      ["HTTP+JSON", "HTTP+JSON"],
                    ]}
                    value={item.protocolBinding ?? ""}
                    hint="Choose a binding this endpoint supports. Add another interface for another binding."
                    validate={agentFieldError}
                    onChange={(protocolBinding) =>
                      edit({
                        supportedInterfaces: interfaces.map((old, at) =>
                          at === index ? { ...old, protocolBinding } : old,
                        ),
                      })
                    }
                  />
                  <NativeChoice
                    id={`${value.editorID}-agent-protocol-${index}`}
                    label={`Interface ${index + 1} A2A protocol version`}
                    customLabel={`Interface ${index + 1} custom A2A protocol version`}
                    choices={[
                      ["1.0", "1.0"],
                      ["0.3", "0.3"],
                    ]}
                    value={item.protocolVersion ?? ""}
                    validate={(next) => agentFieldError(next, "protocol")}
                    hint="This is the endpoint's A2A protocol version, separate from the Agent's Version label."
                    onChange={(protocolVersion) =>
                      edit({
                        supportedInterfaces: interfaces.map((old, at) =>
                          at === index ? { ...old, protocolVersion } : old,
                        ),
                      })
                    }
                  />
                </details>
                <div className="workspace-actions interface-actions">
                  <button
                    type="button"
                    disabled={index === 0}
                    onClick={() => moveInterface(index, index - 1)}
                  >
                    <span className="icon-up">{controlIcons.arrow}</span> Move
                    interface {index + 1} up
                  </button>
                  <button
                    type="button"
                    disabled={index === interfaces.length - 1}
                    onClick={() => moveInterface(index, index + 1)}
                  >
                    <span className="icon-down">{controlIcons.arrow}</span> Move
                    interface {index + 1} down
                  </button>
                  {interfaces.length > 1 && (
                    <button
                      type="button"
                      onClick={() => {
                        interfaceRows.current.keys.splice(index, 1);
                        edit(
                          {
                            supportedInterfaces: interfaces.filter(
                              (_, at) => at !== index,
                            ),
                          },
                          `${value.editorID}-agent-url-${Math.min(index, interfaces.length - 2)}`,
                        );
                      }}
                    >
                      {controlIcons.remove} Remove interface {index + 1}
                    </button>
                  )}
                </div>
              </fieldset>
            ))}
            <button
              type="button"
              onClick={() =>
                edit(
                  {
                    supportedInterfaces: [
                      ...interfaces,
                      ...newAgentCard("").supportedInterfaces!,
                    ],
                  },
                  `${value.editorID}-agent-url-${interfaces.length}`,
                )
              }
            >
              {controlIcons.add} Add interface
            </button>
          </fieldset>
          <div className="agent-media-grid">
            <AgentValues
              id={`${value.editorID}-agent-input`}
              label="Input format"
              value={card.defaultInputModes ?? []}
              onChange={(defaultInputModes) => edit({ defaultInputModes })}
              media
            />
            <AgentValues
              id={`${value.editorID}-agent-output`}
              label="Output format"
              value={card.defaultOutputModes ?? []}
              onChange={(defaultOutputModes) => edit({ defaultOutputModes })}
              media
            />
          </div>
          <fieldset className="workspace-panel workspace-fields">
            <legend>What can this Agent do?</legend>
            <p>
              Describe its abilities. These do not create separate Catalog Skill
              resources.
            </p>
            {abilities.map((item, index) => {
              const patch = (next: Record<string, unknown>) =>
                edit({
                  skills: abilities.map((old, at) =>
                    at === index ? { ...old, ...next } : old,
                  ),
                });
              const suggestion = suggestResourceName(item.name ?? "");
              return (
                <fieldset
                  key={index}
                  className="workspace-panel workspace-fields"
                >
                  <legend>Ability {index + 1}</legend>
                  <AgentField
                    id={`${value.editorID}-agent-ability-name-${index}`}
                    label={`Ability ${index + 1} name`}
                    value={item.name ?? ""}
                    onChange={(name) => patch({ name })}
                  />
                  <AgentField
                    id={`${value.editorID}-agent-ability-id-${index}`}
                    label={`Ability ${index + 1} ID`}
                    value={item.id ?? ""}
                    onChange={(id) => patch({ id })}
                    hint="A stable identifier within this Agent Card."
                  />
                  {suggestion && suggestion !== item.id && (
                    <button
                      type="button"
                      onClick={() => patch({ id: suggestion })}
                    >
                      {controlIcons.edit} Use suggested ability ID: {suggestion}
                    </button>
                  )}
                  <AgentField
                    id={`${value.editorID}-agent-ability-description-${index}`}
                    label={`Ability ${index + 1} description`}
                    value={item.description ?? ""}
                    multiline
                    onChange={(description) => patch({ description })}
                  />
                  <AgentValues
                    id={`${value.editorID}-agent-tags-${index}`}
                    label={`Ability ${index + 1} tag`}
                    value={item.tags ?? []}
                    onChange={(tags) => patch({ tags })}
                  />
                  {abilities.length > 1 && (
                    <button
                      type="button"
                      onClick={() =>
                        edit(
                          {
                            skills: abilities.filter((_, at) => at !== index),
                          },
                          `${value.editorID}-agent-ability-name-${Math.min(index, abilities.length - 2)}`,
                        )
                      }
                    >
                      {controlIcons.remove} Remove ability {index + 1}
                    </button>
                  )}
                </fieldset>
              );
            })}
            <button
              type="button"
              onClick={() =>
                edit(
                  {
                    skills: [
                      ...abilities,
                      { id: "", name: "", description: "", tags: [""] },
                    ],
                  },
                  `${value.editorID}-agent-ability-name-${abilities.length}`,
                )
              }
            >
              {controlIcons.add} Add ability
            </button>
          </fieldset>
          <details>
            <summary>Optional Agent capabilities</summary>
            <p>Select only capabilities that this Agent implements.</p>
            {(
              [
                ["streaming", "Streaming responses"],
                ["pushNotifications", "Push notifications"],
                ["extendedAgentCard", "Authenticated extended Card"],
              ] as const
            ).map(([field, label]) => (
              <Field key={field} label={label}>
                <input
                  type="checkbox"
                  checked={card.capabilities?.[field] === true}
                  onChange={(event) =>
                    edit({
                      capabilities: {
                        ...card.capabilities,
                        [field]: event.target.checked,
                      },
                    })
                  }
                />
              </Field>
            ))}
          </details>
        </fieldset>
      )}
    </div>
  );
}
