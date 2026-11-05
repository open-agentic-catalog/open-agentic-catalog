import type { components } from "../api/schema";
import { parseObject } from "./representations";
import {
  byteText,
  characterText,
  listingLink,
  atField,
  InputError,
} from "./input-validation";
import { Field } from "./workspace";
import { MetadataEditor } from "./MetadataEditor";

type Handoff = {
  git?: components["schemas"]["GitHandoffReference"];
  oci?: components["schemas"]["OCIHandoffReference"];
};
export function handoffShape(value: Record<string, unknown>): Handoff {
  if (Object.keys(value).some((key) => key !== "git" && key !== "oci"))
    throw new Error("Handoff accepts only git and oci references.");
  for (const key of ["git", "oci"] as const) {
    const item = value[key];
    if (item === undefined) continue;
    const fields =
      key === "git" ? ["repository", "commit", "directory"] : ["reference"];
    if (
      !item ||
      typeof item !== "object" ||
      Array.isArray(item) ||
      Object.keys(item).some((field) => !fields.includes(field)) ||
      fields.some(
        (field) => typeof (item as Record<string, unknown>)[field] !== "string",
      )
    )
      throw new Error(
        `Use the documented text fields for the ${key} reference.`,
      );
  }
  return value as Handoff;
}
export function handoffInput(text: string, prefix = "skill-handoff"): Handoff {
  const value = atField(prefix, () => handoffShape(parseObject(text || "{}")));
  const mapped = (field: string, check: () => unknown) => {
    try {
      return check();
    } catch (error) {
      throw new InputError(
        error instanceof Error ? error.message : "Check the handoff.",
        `${prefix}-${field}`,
        prefix,
      );
    }
  };
  if (!value.git && !value.oci)
    throw new Error("Supply at least one immutable Git or OCI reference.");
  if (value.git) {
    const repository = value.git.repository.trim();
    const commit = value.git.commit.trim();
    const directory = value.git.directory.trim();
    mapped("git-repository", () => {
      characterText(repository, "Git repository", 2048, 1);
      listingLink(repository);
      const url = new URL(repository);
      // WHATWG URL parsing resolves dot segments; inspect the supplied path
      // before that normalization, matching the backend's retained-reference checks.
      const path = repository
        .replace(/^[^:]+:\/\/[^/]+/u, "")
        .replace(/\/$/u, "");
      if (
        /[?#%\\]|\p{Cc}/u.test(repository) ||
        /[^\p{ASCII}]/u.test(repository.split("/")[2]!) ||
        url.port === "0" ||
        url.pathname.includes("%") ||
        url.pathname === "/" ||
        path
          .split("/")
          .slice(1)
          .some((part) => part === "." || part === ".." || !part)
      )
        throw new Error(
          "Use a Git repository path without query, fragment, encoding or empty segments.",
        );
    });
    mapped("git-commit", () => {
      if (!/^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$/u.test(commit.trim()))
        throw new Error(
          "Git commit must be a full 40- or 64-character hexadecimal object ID.",
        );
    });
    mapped("git-directory", () => {
      characterText(directory, "Git directory", 1024, 1);
      byteText(directory, "Git directory", 1024);
      const segments = directory.split("/");
      if (
        /^\/|[:\\]|\p{Cc}/u.test(directory) ||
        segments.length > 17 ||
        segments.some(
          (part) =>
            !part ||
            part === "." ||
            part === ".." ||
            new TextEncoder().encode(part).length > 255 ||
            part.endsWith(".") ||
            part.endsWith(" "),
        )
      )
        throw new Error(
          "Git directory must be a normalized relative package path.",
        );
    });
  }
  if (value.oci) {
    mapped("oci-reference", () => {
      characterText(value.oci!.reference.trim(), "OCI reference", 2048, 1);
      byteText(value.oci!.reference.trim(), "OCI reference", 2048);
      if (
        /[^\p{ASCII}]/u.test(value.oci!.reference) ||
        !/^(?:localhost|[^/@\s.:]+(?:[.:][^/@\s.:]+)+|\[[0-9A-Fa-f:.]+\](?::[0-9]+)?)\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*@sha256:[a-f0-9]{64}$/u.test(
          value.oci!.reference.trim(),
        )
      )
        throw new Error(
          "Use an explicit OCI registry/repository@sha256: followed by 64 lowercase hexadecimal characters.",
        );
    });
  }
  return {
    ...(value.git
      ? {
          git: {
            repository: value.git.repository.trim(),
            commit: value.git.commit.trim().toLowerCase(),
            directory: value.git.directory.trim(),
          },
        }
      : {}),
    ...(value.oci ? { oci: { reference: value.oci.reference.trim() } } : {}),
  };
}
export function SkillHandoffEditor({
  id = "skill-handoff",
  value,
  onChange,
}: {
  id?: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <>
      <p>
        Supply at least one immutable Git or OCI reference for the complete
        package; both are allowed. The Catalog retains inspection metadata and
        these references. It does not verify remote availability, clone, pull,
        install or execute the package.
      </p>
      <MetadataEditor
        id={id}
        label="Immutable handoff"
        value={value}
        onChange={onChange}
        validate={handoffShape}
        initiallyAdvanced={!!value}
      >
        {(data, update) => (
          <>
            <Field label="Include Git handoff">
              <input
                type="checkbox"
                checked={!!data.git}
                onChange={(event) => {
                  const next = { ...data };
                  if (event.target.checked)
                    next.git = { repository: "", commit: "", directory: "" };
                  else delete next.git;
                  update(next);
                }}
              />
            </Field>
            {data.git &&
              (["repository", "commit", "directory"] as const).map((key) => (
                <Field
                  key={key}
                  label={`Git ${key}`}
                  hint={
                    key === "commit"
                      ? "Full immutable 40- or 64-character commit."
                      : key === "directory"
                        ? "Relative directory ending with the inspected Skill root."
                        : "Credential-free HTTPS repository URL, without query or fragment."
                  }
                >
                  <input
                    id={`${id}-git-${key}`}
                    required
                    maxLength={
                      key === "repository"
                        ? 4096
                        : key === "commit"
                          ? 128
                          : 2048
                    }
                    value={data.git![key]}
                    onChange={(event) =>
                      update({
                        ...data,
                        git: { ...data.git!, [key]: event.target.value },
                      })
                    }
                  />
                </Field>
              ))}
            <Field label="Include OCI handoff">
              <input
                type="checkbox"
                checked={!!data.oci}
                onChange={(event) => {
                  const next = { ...data };
                  if (event.target.checked) next.oci = { reference: "" };
                  else delete next.oci;
                  update(next);
                }}
              />
            </Field>
            {data.oci && (
              <Field
                label="OCI digest reference"
                hint="Explicit registry/repository@sha256: followed by the lowercase digest; mutable tags are not accepted."
              >
                <input
                  id={`${id}-oci-reference`}
                  required
                  maxLength={4096}
                  value={data.oci.reference}
                  onChange={(event) =>
                    update({ ...data, oci: { reference: event.target.value } })
                  }
                />
              </Field>
            )}
          </>
        )}
      </MetadataEditor>
    </>
  );
}
