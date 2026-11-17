import {
  object,
  text,
  number,
  identifier,
  timestamp,
  page,
} from "../api/values";
import * as checks from "./responses";

export function collection(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.namespaceId) &&
    text(value.name) &&
    [...value.name].length >= 1 &&
    value.name.trim().length > 0 &&
    [...value.name].length <= 160 &&
    text(value.description) &&
    [...value.description].length <= 2000 &&
    number(value.revision, 1) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}
export function collectionPage(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.items) &&
    value.items.length <= 100 &&
    page(collection)(value)
  );
}
export function member(value: unknown): boolean {
  return (
    checks.CatalogSummary(value) &&
    object(value) &&
    value.source === "local" &&
    object(value.listing) &&
    identifier(value.listing.id) &&
    identifier(value.listing.namespaceId) &&
    object(value.versionRevision) &&
    identifier(value.versionRevision.id) &&
    object(value.release) &&
    identifier(value.release.id) &&
    identifier(value.release.listingRevisionId) &&
    identifier(value.release.versionRevisionId) &&
    object(value.listingRevision) &&
    value.release.listingRevisionId === value.listingRevision.id &&
    value.release.versionRevisionId === value.versionRevision.id &&
    object(value.resource) &&
    value.resource.namespaceId === value.listing.namespaceId
  );
}
export function memberPage(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.items) &&
    value.items.length <= 100 &&
    page(member)(value)
  );
}
