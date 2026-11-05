import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
import { describe, expect, it } from "vitest";
import type { Access } from "./client";
import {
  permits,
  permitsNamespace,
  publisherAccess,
  jobActions,
  jobActionReason,
  releaseTransitionAllowed,
  resourceCreationAccess,
  contributionReason,
} from "./permissions";

const access: Access = {
  tenantId: "t",
  membershipId: "m",
  realmId: "r",
  subject: "s",
  actorType: "human",
  ...testAccess([]),
  namespaceGrants: [],
  scopeEnforcement: false,
  scopes: [],
};
describe("Workspace permission hints", () => {
  it("distinguishes known creation authority, incomplete grants and missing scopes", () => {
    expect(resourceCreationAccess(access)).toBe("denied");
    expect(
      resourceCreationAccess({ ...access, ...testAccess(["contributor"]) }),
    ).toBe("allowed");
    expect(
      resourceCreationAccess({ ...access, ...testAccess(["tenant_admin"]) }),
    ).toBe("allowed");
    const paged = {
      ...access,
      availablePermissions: ["resource.create" as const],
      nextCursor: "more",
    };
    expect(resourceCreationAccess(paged)).toBe("choose_namespace");
    expect(
      resourceCreationAccess({
        ...access,
        availablePermissions: ["resource.create"],
        namespaceGrants: [
          { namespaceId: "n", permissions: testPermissions(["contributor"]) },
        ],
      }),
    ).toBe("choose_namespace");
    expect(
      resourceCreationAccess({
        ...access,
        namespaceGrants: [
          { namespaceId: "n", permissions: testPermissions(["reviewer"]) },
        ],
      }),
    ).toBe("denied");
    expect(
      resourceCreationAccess({
        ...paged,
        ...testAccess(["tenant_admin"]),
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      }),
    ).toBe("denied");
    expect(
      contributionReason({ ...access, scopeEnforcement: true }, "n"),
    ).toContain("catalog.write scope");
    expect(contributionReason(access)).toContain(
      "resource.edit permission is required",
    );
    expect(contributionReason(access, "")).toContain("Choose a namespace");
    expect(contributionReason(access, "n")).toContain(
      "resource.edit permission is required in this namespace",
    );
    expect(contributionReason(paged, "n")).toContain("has not been confirmed");
    expect(
      contributionReason(
        {
          ...paged,
          namespaceGrants: [
            { namespaceId: "n", permissions: testPermissions(["reviewer"]) },
          ],
        },
        "n",
      ),
    ).toContain("is required");
    expect(
      contributionReason(
        {
          ...paged,
          namespaceGrants: [
            { namespaceId: "n", permissions: testPermissions(["contributor"]) },
          ],
        },
        "n",
      ),
    ).toBeUndefined();
  });
  it("retains revocation for retired and superseded releases", () => {
    for (const state of ["published", "deprecated", "retired", "superseded"])
      expect(releaseTransitionAllowed(state, "revoke")).toBe(true);
    expect(releaseTransitionAllowed("revoked", "revoke")).toBe(false);
    expect(releaseTransitionAllowed("retired", "supersede")).toBe(false);
    expect(releaseTransitionAllowed("deprecated", "deprecate")).toBe(false);
    expect(releaseTransitionAllowed("unknown", "revoke")).toBe(false);
    expect(releaseTransitionAllowed("published", "deprecate")).toBe(true);
  });
  it.each([
    ["review.decide", "catalog.review"],
    ["release.publish", "catalog.publish"],
    ["release.transition", "catalog.publish"],
  ] as const)(
    "explains the independent %s OAuth ceiling",
    (permission, scope) => {
      const granted: Access = {
        ...access,
        namespaceGrants: [{ namespaceId: "n", permissions: [permission] }],
        scopeEnforcement: true,
        scopes: ["catalog.read", "catalog.write"],
      };
      expect(contributionReason(granted, "n", permission, scope)).toBe(
        `The ${scope} scope is required for this action.`,
      );
      expect(
        contributionReason(
          { ...granted, scopes: [scope] },
          "n",
          permission,
          scope,
        ),
      ).toBeUndefined();
      expect(
        contributionReason(
          { ...granted, scopes: [scope] },
          "other",
          permission,
          scope,
        ),
      ).toContain(`${permission} permission is required in this namespace`);
    },
  );
  it("keeps namespace authorization and scopes independent", () => {
    const scoped: Access = {
      ...access,
      namespaceGrants: [
        { namespaceId: "n", permissions: testPermissions(["contributor"]) },
      ],
    };
    expect(
      permitsNamespace(scoped, "resource.edit", "catalog.write", "n"),
    ).toBe(true);
    expect(
      permitsNamespace(scoped, "resource.edit", "catalog.write", "other"),
    ).toBe(false);
    expect(
      permitsNamespace(scoped, "review.decide", "catalog.write", "n"),
    ).toBe(false);
    expect(
      permitsNamespace(
        { ...scoped, scopeEnforcement: true },
        "resource.edit",
        "catalog.write",
        "n",
      ),
    ).toBe(false);
    expect(
      permitsNamespace(
        { ...access, ...testAccess(["tenant_admin"]) },
        "release.publish",
        "catalog.publish",
        "n",
      ),
    ).toBe(true);
    expect(
      permitsNamespace(
        { ...access, ...testAccess(["tenant_admin"]), scopeEnforcement: true },
        "release.publish",
        "catalog.publish",
        "n",
      ),
    ).toBe(false);
  });
  it("keeps Publisher read and create scopes independent of Catalog browsing", () => {
    const contributor = {
      ...access,
      ...testAccess(["contributor" as const]),
      scopeEnforcement: true,
      scopes: ["publishers.manage" as const],
    };
    expect(publisherAccess(contributor)).toEqual({
      read: false,
      create: true,
      update: false,
    });
    expect(
      publisherAccess({
        ...contributor,
        ...testAccess(["reader"]),
        scopes: ["publishers.read"],
      }),
    ).toEqual({ read: true, create: false, update: false });
    expect(
      publisherAccess({
        ...contributor,
        ...testAccess(["tenant_admin"]),
        scopes: [],
      }),
    ).toEqual({ read: false, create: false, update: false });
  });
  it("does not promote namespace grants to global management authority", () => {
    const scoped = {
      ...access,
      namespaceGrants: [
        {
          namespaceId: "n",
          permissions: testPermissions([
            "contributor" as const,
            "publisher" as const,
          ]),
        },
      ],
    };
    expect(publisherAccess(scoped).create).toBe(false);
    expect(permits(scoped, "access.manage", "tenant.admin")).toBe(false);
    expect(
      jobActions(scoped, {
        kind: "publication.generate",
        state: "failed",
        cancellationRequested: false,
      }).retry,
    ).toBe(false);
  });
  it.each([
    ["source.sync", "source_admin"],
    ["source.trust_verify", "source_admin"],
    ["publication.generate", "publisher"],
    ["semantic.reindex", "tenant_admin"],
  ] as const)(
    "uses the %s Job kind role and catalog.write scope",
    (kind, role) => {
      const job = { kind, state: "failed", cancellationRequested: false };
      expect(jobActions({ ...access, ...testAccess([role]) }, job)).toEqual({
        cancel: false,
        retry: true,
      });
      expect(
        jobActions({ ...access, ...testAccess(["reader"]) }, job).retry,
      ).toBe(false);
      expect(
        jobActions(
          {
            ...access,
            ...testAccess([role]),
            scopeEnforcement: true,
            scopes: ["catalog.publish", "sources.manage"],
          },
          job,
        ).retry,
      ).toBe(false);
      expect(
        jobActions(
          {
            ...access,
            ...testAccess([role]),
            scopeEnforcement: true,
            scopes: ["catalog.write"],
          },
          job,
        ).retry,
      ).toBe(true);
    },
  );
  it.each([
    ["cancel", "running", "job.cancel"],
    ["retry", "failed", "job.retry"],
  ] as const)(
    "requires each tenant permission independently for ANS Job %s",
    (action, state, operation) => {
      const permissions: Access["permissions"] = [
        "source.read_evidence",
        "source.manage",
        "federation.discover",
        operation,
      ];
      const permitted: Access = {
        ...access,
        permissions,
        availablePermissions: permissions,
        scopeEnforcement: true,
        // Source-route ceilings are not Job command ceilings.
        scopes: ["catalog.write"],
      };
      const job = {
        kind: "source.trust_verify",
        state,
        cancellationRequested: false,
      };
      expect(jobActions(permitted, job)[action]).toBe(true);
      for (const missing of permissions) {
        const limited = {
          ...permitted,
          permissions: permissions.filter((value) => value !== missing),
          namespaceGrants: [{ namespaceId: "n", permissions: [missing] }],
        };
        expect(jobActions(limited, job)[action], missing).toBe(false);
      }
      expect(jobActions({ ...permitted, scopes: [] }, job)[action]).toBe(false);
      expect(
        jobActions({ ...permitted, scopeEnforcement: false, scopes: [] }, job)[
          action
        ],
      ).toBe(true);
    },
  );
  it.each([
    ["source.sync", "source.manage"],
    ["publication.generate", "publication.generate"],
    ["semantic.reindex", "operations.reindex"],
    ["source.trust_verify", "source.read_evidence"],
  ] as const)(
    "explains the missing %s Job family permission",
    (kind, permission) => {
      const job = { kind, state: "running", cancellationRequested: false };
      const hints: Access = {
        ...access,
        permissions: ["job.cancel"],
        availablePermissions: [permission, "job.cancel"],
        namespaceGrants: [{ namespaceId: "n", permissions: [permission] }],
      };
      expect(jobActionReason(hints, job, "cancel")).toBe(
        `The ${permission} permission is required.`,
      );
      expect(
        jobActionReason({ ...hints, scopeEnforcement: true }, job, "cancel"),
      ).toBe("The catalog.write OAuth scope is required.");
    },
  );
  it.each(["cancel", "retry"] as const)(
    "explains missing ANS companions and the exact Job %s permission",
    (action) => {
      const job = {
        kind: "source.trust_verify",
        state: action === "cancel" ? "running" : "failed",
        cancellationRequested: false,
      };
      const required: Access["permissions"] = [
        "source.read_evidence",
        "source.manage",
        "federation.discover",
        action === "cancel" ? "job.cancel" : "job.retry",
      ];
      const permitted: Access = { ...access, permissions: required };
      expect(jobActionReason(permitted, job, action)).toBeUndefined();
      for (const companion of [
        "source.manage",
        "federation.discover",
      ] as const) {
        expect(
          jobActionReason(
            {
              ...permitted,
              permissions: required.filter((p) => p !== companion),
            },
            job,
            action,
          ),
        ).toBe(
          "ANS Job commands also require source.manage and federation.discover.",
        );
      }
      expect(
        jobActionReason(
          {
            ...permitted,
            permissions: required.filter((p) => p !== `job.${action}`),
          },
          job,
          action,
        ),
      ).toBe(`The job.${action} permission is required.`);
    },
  );
  it.each(["source.sync", "source.trust_verify", "unknown.kind"])(
    "keeps %s actions bounded by state and cancellation",
    (kind) => {
      const admin = { ...access, ...testAccess(["tenant_admin" as const]) };
      for (const [state, cancel, retry] of [
        ["queued", true, false],
        ["running", true, false],
        ["waiting", true, false],
        ["failed", false, true],
        ["dead_letter", false, true],
        ["succeeded", false, false],
        ["cancelled", false, false],
        ["unexpected", false, false],
      ] as const) {
        expect(
          jobActions(admin, { kind, state, cancellationRequested: false }),
          state,
        ).toEqual({
          cancel: kind !== "unknown.kind" && cancel,
          retry: kind !== "unknown.kind" && retry,
        });
      }
      expect(
        jobActions(admin, {
          kind,
          state: "running",
          cancellationRequested: true,
        }).cancel,
      ).toBe(false);
    },
  );
});
