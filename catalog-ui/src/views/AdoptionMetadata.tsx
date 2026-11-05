import type { components } from "../api/schema";
import { characterText, listingLink, metadataTerms } from "./input-validation";
import { MetadataEditor } from "./MetadataEditor";
import { ValueList } from "./ValueList";
import { Field } from "./workspace";

type Overrides = Omit<
  components["schemas"]["AdoptObservation"],
  | "snapshotId"
  | "retrievalDigest"
  | "entryLocation"
  | "namespaceId"
  | "publisherId"
>;
const textFields = [
  "version",
  "displayName",
  "description",
  "documentationUrl",
  "supportUrl",
  "license",
  "termsUrl",
] as const;
const labels = {
  version: "Local version",
  displayName: "Local display name",
  description: "Local description",
  documentationUrl: "Local documentation URL",
  supportUrl: "Local support URL",
  license: "Local license",
  termsUrl: "Local terms URL",
};
const lengths = {
  version: 128,
  displayName: 160,
  description: 4000,
  documentationUrl: 2048,
  supportUrl: 2048,
  termsUrl: 2048,
};

/** Closed editable shape; semantic validation is applied only when preparing a command. */
export function adoptionShape(value: Record<string, unknown>): Overrides {
  if (
    Object.keys(value).some(
      (key) => ![...textFields, "categories", "visibility"].includes(key),
    )
  )
    throw new Error("Use only the documented local metadata fields.");
  for (const key of textFields)
    if (key in value && typeof value[key] !== "string")
      throw new Error(`${labels[key]} must be text.`);
  if (
    "categories" in value &&
    (!Array.isArray(value.categories) ||
      !value.categories.every((item) => typeof item === "string"))
  )
    throw new Error("Categories must be a list of text values.");
  if (
    "visibility" in value &&
    (typeof value.visibility !== "string" ||
      !["private", "tenant", "public"].includes(value.visibility))
  )
    throw new Error("Choose private, tenant or public visibility.");
  return value as Overrides;
}
export function adoptionInput(value: Record<string, unknown>): Overrides {
  const shaped = adoptionShape(value);
  for (const key of textFields) {
    const text = shaped[key];
    if (text === undefined) continue;
    if (key === "license") {
      if (text.includes("\0"))
        throw new Error("Local license must not contain NUL.");
    } else
      characterText(
        text,
        labels[key],
        lengths[key],
        ["version", "displayName", "description"].includes(key) ? 1 : 0,
      );
    if (key.endsWith("Url")) listingLink(text);
  }
  if (shaped.categories !== undefined) {
    metadataTerms(shaped.categories, "categories", 16);
    if (new Set(shaped.categories).size !== shaped.categories.length)
      throw new Error("Categories must be distinct.");
  }
  return shaped;
}
export function AdoptionMetadata({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <MetadataEditor
      id="adoption-overrides"
      value={value}
      onChange={onChange}
      label="Local metadata overrides"
      validate={adoptionShape}
    >
      {(data, update) => (
        <>
          <p>
            Unchecked fields are omitted. Version is required only if absent
            upstream; otherwise omit it or enter the observed version. Name and
            description are required if absent upstream. The Catalog checks
            these conditions against the exact occurrence when you confirm; no
            raw evidence is fetched automatically.
          </p>
          {textFields.map((key) => (
            <div key={key}>
              <Field label={`Override ${labels[key].toLowerCase()}`}>
                <input
                  type="checkbox"
                  checked={data[key] !== undefined}
                  onChange={(event) => {
                    const next = { ...data };
                    if (event.target.checked) next[key] = "";
                    else delete next[key];
                    update(next);
                  }}
                />
              </Field>
              {data[key] !== undefined && (
                <Field
                  label={labels[key]}
                  hint="Uncheck the override to omit this field; an empty value is not omission."
                >
                  <textarea
                    rows={key === "description" ? 4 : 1}
                    required={[
                      "version",
                      "displayName",
                      "description",
                    ].includes(key)}
                    maxLength={key === "license" ? undefined : lengths[key] * 2}
                    value={data[key]}
                    onChange={(event) =>
                      update({ ...data, [key]: event.target.value })
                    }
                  />
                </Field>
              )}
            </div>
          ))}
          <Field label="Override local categories">
            <input
              type="checkbox"
              checked={data.categories !== undefined}
              onChange={(event) => {
                const next = { ...data };
                if (event.target.checked) next.categories = [];
                else delete next.categories;
                update(next);
              }}
            />
          </Field>
          {data.categories !== undefined && (
            <ValueList
              id="adoption-categories"
              label="Local categories"
              itemLabel="Local category"
              value={data.categories}
              onChange={(categories) => update({ ...data, categories })}
              maximum={16}
              length={80}
              hint="An enabled empty list explicitly supplies no categories."
            />
          )}
          <Field
            label="Local visibility"
            hint="Private is the default. Publication and review remain independent."
          >
            <select
              value={data.visibility ?? ""}
              onChange={(event) => {
                const next = { ...data };
                if (event.target.value)
                  next.visibility = event.target.value as NonNullable<
                    Overrides["visibility"]
                  >;
                else delete next.visibility;
                update(next);
              }}
            >
              <option value="">Omit (default private)</option>
              <option value="private">private</option>
              <option value="tenant">tenant</option>
              <option value="public">public</option>
            </select>
          </Field>
        </>
      )}
    </MetadataEditor>
  );
}
