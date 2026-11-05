import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { developmentUsers } from "./development-users.mjs";

test("copies only explicit presentation, with literal aliases and private tokens", () => {
  assert.deepEqual(
    developmentUsers({
      exact: {
        subject: "subject",
        label: "Choice",
        description: "Help",
        profileGroup: "template",
        elevated: false,
        separateTenant: true,
        roles: ["tenant_admin"],
      },
    }),
    [
      {
        alias: "exact",
        label: "Choice",
        description: "Help",
        profileGroup: "template",
        elevated: false,
        separateTenant: true,
        token: "exact",
      },
    ],
  );
  assert.deepEqual(
    developmentUsers({ custom: { subject: "Fallback", label: "" } }),
    [{ alias: "custom", label: "Fallback", token: "custom" }],
  );
});

test("both maintained twenty-profile rosters preserve six bundles and fourteen exact choices", () => {
  for (const file of ["local.json", "postgres.json"]) {
    const tokens = JSON.parse(
      readFileSync(new URL(`../../config/${file}`, import.meta.url)),
    ).auth.developmentTokens;
    const users = developmentUsers(tokens);
    assert.equal(users.length, 20);
    assert.equal(
      users.filter((user) => user.profileGroup === "bundle").length,
      6,
    );
    assert.equal(
      users.filter((user) => user.profileGroup === "template").length,
      14,
    );
    assert.deepEqual(
      users.filter((user) => user.elevated).map((user) => user.alias),
      ["local-admin", "other-tenant-admin", "local-template-tenant-admin"],
    );
    assert.deepEqual(
      users.filter((user) => user.separateTenant).map((user) => user.alias),
      ["other-tenant-admin"],
    );
    assert.match(
      users.find((user) => user.alias === "local-template-reader").description,
      /ratings/,
    );
    assert.match(
      users.find((user) => user.alias === "local-template-discovery-reader")
        .description,
      /No local Resource reads or operational Jobs/,
    );
    assert.equal(
      users.find((user) => user.alias === "local-template-service-client")
        .label,
      "Service client (exact template; nonhuman)",
    );
  }
});
