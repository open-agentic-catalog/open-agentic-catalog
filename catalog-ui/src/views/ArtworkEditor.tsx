import { controlIcons } from "../shell/Icon";
import { useId, useLayoutEffect, useRef } from "react";
import { Field } from "./workspace";
import {
  maximumAuthorScreenshots,
  type ArtworkAsset,
  type ArtworkDescriptor,
  type NativeIcon,
} from "./artwork";

const emptyAsset = (): ArtworkAsset => ({
  url: "",
  alt: "",
  rights: { basis: "owned", attribution: "" },
});
export function ArtworkAssetFields({
  value,
  onChange,
  label,
  candidates = [],
  fieldId,
}: {
  fieldId?: string;
  value: ArtworkAsset;
  onChange: (value: ArtworkAsset) => void;
  label: string;
  candidates?: NativeIcon[];
}) {
  const generatedId = useId();
  const id = fieldId ?? generatedId;
  const update = <K extends keyof ArtworkAsset>(
    key: K,
    next: ArtworkAsset[K],
  ) => onChange({ ...value, [key]: next });
  return (
    <fieldset className="artwork-editor">
      <legend>{label}</legend>
      {candidates.length > 0 && (
        <Field
          label="Use a declared native icon"
          hint="This selects a URL only. Declare your rights and attribution before saving."
        >
          <select
            value=""
            onChange={(event) => {
              if (event.target.value) update("url", event.target.value);
            }}
          >
            <option value="">Choose a theme and size</option>
            {candidates.map((item, index) => (
              <option key={`${item.url}-${index}`} value={item.url}>
                {item.label}: {item.url}
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field
        label={`${label} HTTPS URL`}
        hint="Use a static PNG or JPEG from an allowed HTTPS origin. The URL must not contain credentials, spaces or a fragment (#)."
      >
        <input
          id={`${id}-url`}
          type="url"
          value={value.url}
          onChange={(event) => update("url", event.target.value)}
        />
      </Field>
      <Field
        label={`${label} alternative text`}
        hint="Describe the image for readers who cannot see it."
      >
        <input
          id={`${id}-alt`}
          value={value.alt}
          onChange={(event) => update("alt", event.target.value)}
        />
      </Field>
      <Field label={`${label} caption (optional)`}>
        <input
          id={`${id}-caption`}
          value={value.caption || ""}
          onChange={(event) => update("caption", event.target.value)}
        />
      </Field>
      <Field
        label={`${label} rights basis`}
        hint="Choose why you may display and publish this image."
        help="Owned means you own the image; licensed means its license allows this use; permission means the rights holder granted this use. The Catalog does not verify legal clearance."
      >
        <select
          id={`${id}-basis`}
          value={value.rights.basis}
          onChange={(event) =>
            update("rights", {
              ...value.rights,
              basis: event.target.value as ArtworkAsset["rights"]["basis"],
            })
          }
        >
          {!["owned", "licensed", "permission"].includes(
            value.rights.basis,
          ) && (
            <option value={value.rights.basis}>
              {value.rights.basis || "Select rights basis"}
            </option>
          )}
          <option value="owned">I own this image</option>
          <option value="licensed">Licensed use</option>
          <option value="permission">Permission granted</option>
        </select>
      </Field>
      <Field
        label={`${label} attribution`}
        hint="Required even for an owned image. For example: © Example team, CC BY 4.0."
      >
        <input
          id={`${id}-attribution`}
          value={value.rights.attribution}
          onChange={(event) =>
            update("rights", {
              ...value.rights,
              attribution: event.target.value,
            })
          }
        />
      </Field>
      <Field
        label={`${label} rights reference (optional)`}
        hint="An HTTPS license or permission reference, without credentials or a fragment (#). Do not include private proof or secrets."
        help="For example: https://example.org/image-license. This is a reference link, not an evidence upload. The Catalog does not fetch it or verify your rights."
      >
        <input
          type="url"
          id={`${id}-reference`}
          value={value.rights.reference || ""}
          onChange={(event) =>
            update("rights", { ...value.rights, reference: event.target.value })
          }
        />
      </Field>
    </fieldset>
  );
}
/** Editor identities stay local; moving equal URLs must not move keyboard ownership. */
export function ArtworkEditor({
  value,
  onChange,
  candidates = [],
  id = "listing-artwork",
}: {
  value: ArtworkDescriptor;
  onChange: (value: ArtworkDescriptor) => void;
  candidates?: NativeIcon[];
  id?: string;
}) {
  const container = useRef<HTMLDetailsElement>(null);
  const rowIds = useRef<string[]>([]);
  while (rowIds.current.length < (value.screenshots?.length ?? 0))
    rowIds.current.push(crypto.randomUUID());
  rowIds.current.length = value.screenshots?.length ?? 0;
  const focus = useRef("");
  useLayoutEffect(() => {
    if (!focus.current) return;
    container.current?.querySelector<HTMLElement>(focus.current)?.focus();
    focus.current = "";
  }, [value]);
  const populated = (asset: ArtworkAsset) =>
    !!(
      asset.url ||
      asset.alt ||
      asset.caption ||
      asset.rights.attribution ||
      asset.rights.reference
    );
  const discard = (asset: ArtworkAsset) =>
    !populated(asset) ||
    window.confirm("Remove this artwork? Its unsaved fields will be lost.");
  return (
    <details ref={container}>
      <summary>Icon and screenshots</summary>
      <p>
        Optional artwork is fetched by the Catalog from allowed HTTPS origins
        when you save. Declare rights for display and downstream publication.
        Validation does not establish legal clearance. The server permits four
        screenshots by default; configured limits apply.
      </p>
      {value.icon ? (
        <div data-artwork-row="icon">
          <ArtworkAssetFields
            label="Icon"
            fieldId={`${id}-icon`}
            value={value.icon}
            candidates={candidates}
            onChange={(icon) => onChange({ ...value, icon })}
          />
          <button
            type="button"
            onClick={() => {
              if (!discard(value.icon!)) return;
              const next = { ...value };
              delete next.icon;
              focus.current = "[data-add-icon]";
              onChange(next);
            }}
          >
            {controlIcons["close"]} Remove icon
          </button>
        </div>
      ) : (
        <button
          data-add-icon
          type="button"
          onClick={() => {
            focus.current = '[data-artwork-row="icon"] input';
            onChange({ ...value, icon: emptyAsset() });
          }}
        >
          {controlIcons["add"]} Add icon
        </button>
      )}
      {(value.screenshots ?? []).map((item, index) => (
        <div
          key={rowIds.current[index]}
          data-artwork-row={rowIds.current[index]}
        >
          <ArtworkAssetFields
            label={`Screenshot ${index + 1}`}
            fieldId={`${id}-screenshot-${index}`}
            value={item}
            onChange={(asset) =>
              onChange({
                ...value,
                screenshots: value.screenshots!.map((v, i) =>
                  i === index ? asset : v,
                ),
              })
            }
          />
          <div className="workspace-toolbar">
            <button
              type="button"
              onClick={() => {
                if (!discard(item)) return;
                rowIds.current.splice(index, 1);
                const survivor =
                  rowIds.current[Math.min(index, rowIds.current.length - 1)];
                focus.current = survivor
                  ? `[data-artwork-row="${survivor}"] input`
                  : "[data-add-screenshot]";
                onChange({
                  ...value,
                  screenshots: value.screenshots!.filter((_, i) => i !== index),
                });
              }}
            >
              {controlIcons["close"]} Remove screenshot {index + 1}
            </button>
            {index > 0 && (
              <button
                data-move-artwork
                className="icon-up"
                type="button"
                onClick={() => {
                  const screenshots = [...value.screenshots!];
                  const ids = rowIds.current;
                  [screenshots[index - 1], screenshots[index]] = [
                    screenshots[index]!,
                    screenshots[index - 1]!,
                  ];
                  [ids[index - 1], ids[index]] = [ids[index]!, ids[index - 1]!];
                  focus.current = `[data-artwork-row="${ids[index - 1]}"] ${index > 1 ? "[data-move-artwork]" : "input"}`;
                  onChange({ ...value, screenshots });
                }}
              >
                {controlIcons["arrow"]} Move screenshot {index + 1} earlier
              </button>
            )}
          </div>
        </div>
      ))}
      <button
        data-add-screenshot
        type="button"
        disabled={(value.screenshots?.length ?? 0) >= maximumAuthorScreenshots}
        onClick={() => {
          const key = crypto.randomUUID();
          rowIds.current.push(key);
          focus.current = `[data-artwork-row="${key}"] input`;
          onChange({
            ...value,
            screenshots: [...(value.screenshots ?? []), emptyAsset()],
          });
        }}
      >
        {controlIcons["add"]} Add screenshot
      </button>
    </details>
  );
}
