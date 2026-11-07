// Artwork values are checked before they drive image reads or editable fields.
import { object, optional } from "../api/values";
import {
  characterText,
  byteText,
  atField,
  InputError,
} from "./input-validation";
export const artworkIRI =
  "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor";
export const maximumAuthorScreenshots = 8;
export type ArtworkAsset = {
  url: string;
  alt: string;
  caption?: string;
  rights: {
    basis: "owned" | "licensed" | "permission";
    attribution: string;
    reference?: string;
  };
  digest?: string;
  mediaType?: string;
  size?: number;
  width?: number;
  height?: number;
};
export type ArtworkDescriptor = {
  schemaVersion: 1;
  icon?: ArtworkAsset;
  screenshots?: ArtworkAsset[];
};
export type NativeIcon = {
  url: string;
  label: string;
};
export function imageURL(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    value.includes("#") ||
    new TextEncoder().encode(value).length > 2048 ||
    [...value].some(
      (character) =>
        character.codePointAt(0)! <= 32 || character.codePointAt(0) === 127,
    )
  )
    return false;
  try {
    const url = new URL(value);
    return (
      url.protocol === "https:" && !url.username && !url.password && !url.hash
    );
  } catch {
    return false;
  }
}
const bounded = (value: unknown, max: number, minimum = 0): value is string =>
  typeof value === "string" &&
  !value.includes("\0") &&
  [...value].length >= minimum &&
  [...value].length <= max;
export function isArtworkAsset(
  value: unknown,
  mutable = false,
): value is ArtworkAsset {
  return (
    object(value) &&
    imageURL(value.url) &&
    bounded(value.alt, 256, mutable ? 0 : 1) &&
    optional(value.caption, (_captionValue) => bounded(_captionValue, 512)) &&
    ((mutable && value.rights === undefined) ||
      (object(value.rights) &&
        ["owned", "licensed", "permission"].includes(
          String(value.rights.basis),
        ) &&
        bounded(value.rights.attribution, 1024, 1) &&
        optional(value.rights.reference, imageURL))) &&
    (optional(value.digest, (_digestValue) => mutable && _digestValue === "") ||
      (typeof value.digest === "string" &&
        /^sha256:[a-f0-9]{64}$/u.test(value.digest))) &&
    (optional(
      value.mediaType,
      (_mediaTypeValue) => mutable && _mediaTypeValue === "",
    ) ||
      ["image/png", "image/jpeg"].includes(String(value.mediaType))) &&
    [value.size, value.width, value.height].every(
      (item) =>
        item === undefined ||
        (typeof item === "number" &&
          Number.isSafeInteger(item) &&
          item >= (mutable ? 0 : 1)),
    )
  );
}

export function isArtwork(
  value: unknown,
  mutableIcon = false,
): value is ArtworkDescriptor {
  return (
    object(value) &&
    value.schemaVersion === 1 &&
    optional(value.icon, (_iconValue) =>
      isArtworkAsset(_iconValue, mutableIcon),
    ) &&
    optional(
      value.screenshots,
      (_screenshotsValue) =>
        Array.isArray(_screenshotsValue) &&
        _screenshotsValue.length <= 8 &&
        _screenshotsValue.every((asset) => isArtworkAsset(asset)),
    )
  );
}
/** Strip sealed backend facts before preparing an authoring command. */
export function artworkInput(
  value: ArtworkDescriptor,
  prefix = "listing-artwork",
): ArtworkDescriptor {
  function asset(item: ArtworkAsset, id: string): ArtworkAsset {
    if (!imageURL(item.url))
      throw new InputError(
        "Artwork needs a credential-free HTTPS URL.",
        `${id}-url`,
      );
    atField(`${id}-alt`, () =>
      characterText(item.alt, "Artwork alternative text", 256, 1),
    );
    atField(`${id}-attribution`, () =>
      characterText(item.rights.attribution, "Artwork attribution", 1024, 1),
    );
    atField(`${id}-url`, () => byteText(item.url, "Artwork URL", 2048));
    if (item.caption)
      atField(`${id}-caption`, () =>
        characterText(item.caption!, "Artwork caption", 512),
      );
    if (item.rights.reference && !imageURL(item.rights.reference))
      throw new InputError(
        "Rights reference needs a credential-free HTTPS URL.",
        `${id}-reference`,
      );
    if (!["owned", "licensed", "permission"].includes(item.rights.basis))
      throw new InputError("Declare an artwork rights basis.", `${id}-basis`);
    return {
      url: item.url,
      alt: item.alt,
      ...(item.caption
        ? {
            caption: item.caption,
          }
        : {}),
      rights: {
        basis: item.rights.basis,
        attribution: item.rights.attribution,
        ...(item.rights.reference
          ? {
              reference: item.rights.reference,
            }
          : {}),
      },
    };
  }
  if ((value.screenshots?.length || 0) > maximumAuthorScreenshots)
    throw new Error("Use up to eight screenshots.");
  return {
    schemaVersion: 1,
    ...(value.icon
      ? {
          icon: asset(value.icon, `${prefix}-icon`),
        }
      : {}),
    ...(value.screenshots?.length
      ? {
          screenshots: value.screenshots.map((item, index) =>
            asset(item, `${prefix}-screenshot-${index}`),
          ),
        }
      : {}),
  };
}
/** Read icon hints only from already loaded native metadata, never from remote URLs. */
export function nativeIcons(value: unknown): NativeIcon[] {
  if (!object(value)) return [];
  const result: NativeIcon[] = [];
  if (imageURL(value.iconUrl))
    result.push({
      url: value.iconUrl,
      label: "Declared native icon",
    });
  if (Array.isArray(value.icons))
    for (const icon of value.icons.slice(0, 16)) {
      if (
        !object(icon) ||
        !imageURL(icon.src) ||
        (icon.mimeType !== undefined &&
          icon.mimeType !== "image/png" &&
          icon.mimeType !== "image/jpeg")
      )
        continue;
      const theme = bounded(icon.theme, 16) ? icon.theme : "any theme";
      const sizes = Array.isArray(icon.sizes)
        ? icon.sizes
            .filter((size) => bounded(size, 32))
            .slice(0, 4)
            .join(", ")
        : "size unspecified";
      result.push({
        url: icon.src,
        label: `${theme}; ${sizes}`,
      });
    }
  return result.slice(0, 16);
}
