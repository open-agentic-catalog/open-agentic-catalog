import { createHash } from "node:crypto";
import { ClientError, record, safeURL } from "./config.js";
import type { Secrets } from "./session.js";
import type { PackageSource } from "./packages.js";

export interface PackageCredential {
  origin: string;
  type: PackageSource["type"];
  scheme: "basic" | "bearer";
  username?: string;
  token: string;
}
export function packageOrigin(source: PackageSource): string {
  const value =
    source.type === "git"
      ? source.repository
      : source.type === "https"
        ? source.url
        : `https://${source.reference.split("@")[0]}`;
  const url = new URL(value);
  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  )
    throw new ClientError("invalid_package_credential_origin");
  return url.origin;
}
export function packageCredential(value: unknown): PackageCredential {
  const v = record(value);
  if (
    Object.keys(v).some(
      (key) => !["type", "origin", "scheme", "username", "token"].includes(key),
    ) ||
    !["git", "oci", "https"].includes(v.type as string) ||
    !["basic", "bearer"].includes(v.scheme as string) ||
    (v.type !== "https" && v.scheme !== "basic") ||
    typeof v.origin !== "string" ||
    safeURL(v.origin, false) !== v.origin ||
    typeof v.token !== "string" ||
    !v.token ||
    Buffer.byteLength(v.token) > 8192 ||
    /[\p{Cc}\p{Cs}]/u.test(v.token) ||
    (v.scheme === "basic" &&
      (typeof v.username !== "string" ||
        !v.username ||
        v.username.length > 256 ||
        /[:\p{Cc}\p{Cs}]/u.test(v.username))) ||
    (v.scheme === "bearer" &&
      (v.username !== undefined || !/^[A-Za-z0-9\-._~+/]+=*$/u.test(v.token)))
  )
    throw new ClientError("invalid_package_credentials");
  return v as unknown as PackageCredential;
}
export function packageAuthorization(
  credential: PackageCredential,
  source: PackageSource,
): string {
  const valid = packageCredential(credential);
  if (valid.type !== source.type || valid.origin !== packageOrigin(source))
    throw new ClientError("package_credential_origin_mismatch");
  return valid.scheme === "bearer"
    ? `Bearer ${valid.token}`
    : `Basic ${Buffer.from(`${valid.username}:${valid.token}`).toString("base64")}`;
}
export class PackageCredentials {
  constructor(private secrets: Secrets) {}
  private async keys(): Promise<string[]> {
    if (!this.secrets.keys)
      throw new ClientError("package_credential_index_unavailable");
    return (await this.secrets.keys()).filter((key) =>
      /^openAgenticCatalog\.package:[a-f0-9]{64}:[a-f0-9]{64}$/u.test(key),
    );
  }
  private key(account: string, type: PackageSource["type"], origin: string) {
    if (!/^[a-f0-9]{64}$/u.test(account))
      throw new ClientError("invalid_package_credentials");
    return `openAgenticCatalog.package:${account}:${createHash("sha256")
      .update(JSON.stringify([type, origin]))
      .digest("hex")}`;
  }
  async get(account: string, source: PackageSource) {
    const origin = packageOrigin(source),
      value = await this.secrets.get(this.key(account, source.type, origin));
    if (value === undefined) return;
    let credential: PackageCredential;
    try {
      credential = packageCredential(JSON.parse(value));
    } catch {
      throw new ClientError("invalid_package_credentials");
    }
    packageAuthorization(credential, source);
    return credential;
  }
  async save(account: string, value: PackageCredential) {
    const credential = packageCredential(value);
    const key = this.key(account, credential.type, credential.origin),
      keys = await this.keys();
    if (!keys.includes(key) && keys.length >= 50)
      throw new ClientError("package_credential_limit");
    await this.secrets.store(key, JSON.stringify(credential));
  }
  async forget(account: string, type: PackageSource["type"], origin: string) {
    await this.secrets.delete(this.key(account, type, safeURL(origin, false)));
  }
  async forgetAll(cancelled: () => boolean = () => false) {
    let deleted = 0,
      failed = 0;
    for (const key of (await this.keys()).slice(0, 50)) {
      if (cancelled()) break;
      try {
        await this.secrets.delete(key);
        deleted++;
      } catch {
        failed++;
      }
    }
    return { deleted, failed, remaining: (await this.keys()).length };
  }
}
