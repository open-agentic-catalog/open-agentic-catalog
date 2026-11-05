// Export declarations without sealed facts or Save-time validation of incomplete fields.
import type { ArtworkDescriptor, ArtworkAsset } from "./artwork";
import type { ListingSnapshot } from "./draft-editor";
export function draftArtwork(
  value: ArtworkDescriptor,
): NonNullable<ListingSnapshot["artwork"]> {
  const asset = ({ url, alt, caption, rights }: ArtworkAsset) => ({
    url,
    alt,
    ...(caption !== undefined ? { caption } : {}),
    ...(rights
      ? {
          rights: {
            basis: rights.basis,
            attribution: rights.attribution,
            ...(rights.reference !== undefined
              ? { reference: rights.reference }
              : {}),
          },
        }
      : {}),
  });
  return {
    schemaVersion: 1,
    ...(value.icon ? { icon: asset(value.icon) } : {}),
    ...(value.screenshots ? { screenshots: value.screenshots.map(asset) } : {}),
  };
}

/** Missing declarations remain empty; importing never claims ownership. */
export function editorArtwork(
  value: ListingSnapshot["artwork"],
): ArtworkDescriptor {
  const asset = (
    item: NonNullable<NonNullable<ListingSnapshot["artwork"]>["icon"]>,
  ): ArtworkAsset => ({
    ...item,
    rights: {
      basis: (item.rights?.basis ?? "") as ArtworkAsset["rights"]["basis"],
      attribution: item.rights?.attribution ?? "",
      ...(item.rights?.reference !== undefined
        ? { reference: item.rights.reference }
        : {}),
    },
  });
  return {
    schemaVersion: 1,
    ...(value?.icon ? { icon: asset(value.icon) } : {}),
    ...(value?.screenshots
      ? { screenshots: value.screenshots.map(asset) }
      : {}),
  };
}
