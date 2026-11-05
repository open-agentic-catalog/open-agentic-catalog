// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from "@testing-library/react";
import { operations, type Document } from "../api/reference";
import { required } from "../../tests/required";
import { OperationContract } from "./OperationContract";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function show(document: Document, operation: Document) {
  const onReference = vi.fn();
  const item = required(
    operations({
      ...document,
      paths: { "/items/{id}": { post: operation } },
    })[0],
  );
  const view = render(
    <OperationContract
      document={document}
      operation={item}
      onReference={onReference}
    />,
  );
  return { ...view, onReference };
}

it.each([
  [[], "No API authentication requirement."],
  [
    [{}, { bearer: [] }],
    "Anonymous or authenticated alternatives are declared.",
  ],
  [[{ bearer: [] }], "API authentication is required."],
  [null, "Authentication details unavailable. Do not assume anonymous access."],
])(
  "explains declared authentication %j without checking permissions or executing an endpoint",
  (security, message) => {
    const { onReference } = show(
      {
        components: {
          securitySchemes: {
            bearer: {
              type: "http",
              scheme: "bearer",
              description: "OIDC access token",
            },
          },
        },
      },
      { security },
    );
    expect(screen.getByText(message)).toBeTruthy();
    expect(
      screen.getByText(/Empty requirement arrays do not bypass OAuth ceilings/),
    ).toBeTruthy();
    expect(onReference).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("button", { name: "Copy operation ID" }),
    ).toBeNull();
  },
);

it("presents inherited field facts, exact copies, local references and complete unknown declarations", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const supplied = '  {"reason":"keep whitespace"}\r\n';
  const document = {
    components: {
      securitySchemes: { bearer: { type: "http", scheme: "bearer" } },
      parameters: {
        Key: {
          name: "Idempotency-Key",
          in: "header",
          required: true,
          description: "One exact command key",
          schema: { type: "string", minLength: 1, maxLength: 200 },
          example: "key-example",
          style: "simple",
          "x-extra": { kept: true },
        },
      },
      schemas: {
        ID: { type: "string", format: "uuid" },
        Job: { type: "object" },
      },
      requestBodies: {
        Input: {
          required: true,
          description: "Declared request",
          content: {
            "application/json": {
              schema: { type: "object", default: false },
              example: supplied,
              examples: {
                zero: { value: 0 },
                external: {
                  externalValue: "https://unfetched.invalid/example",
                },
                linked: { $ref: "#/components/examples/Text" },
              },
            },
          },
        },
      },
      examples: {
        Text: {
          description: "Exact named value",
          value: "<script>inert()</script>",
        },
      },
    },
  };
  const { container, onReference } = show(document, {
    operationId: "createItem",
    security: [{ bearer: ["sources.manage"] }],
    parameters: [
      {
        name: "id",
        in: "path",
        required: true,
        schema: { $ref: "#/components/schemas/ID" },
      },
      { $ref: "#/components/parameters/Key" },
      {
        name: "limit",
        in: "query",
        required: false,
        schema: { type: "integer", minimum: 0, default: 0 },
        examples: { tiny: { value: false } },
        content: { "text/plain": { schema: true } },
      },
    ],
    requestBody: {
      $ref: "#/components/requestBodies/Input",
      description: "Outer request description",
    },
    responses: {
      "202": {
        description: "Job accepted, not completed",
        content: {
          "application/json": {
            schema: { $ref: "#/components/schemas/Job" },
            example: { jobId: "example" },
          },
        },
        headers: {
          ETag: {
            description: "Exact current revision",
            schema: { type: "string" },
          },
        },
      },
      "204": { description: "No content" },
      "4XX": { $ref: "#/missing" },
      default: {
        description: "Control problem",
        content: {
          "application/problem+json": {
            schema: { type: "object" },
            examples: {
              missing: null,
              ambiguous: {
                value: false,
                externalValue: "https://unfetched.invalid",
              },
            },
          },
        },
      },
    },
  });
  expect(screen.getByText("HTTP Bearer token")).toBeTruthy();
  expect(
    screen.getByText(/Declared requirement values:/).textContent,
  ).toContain("sources.manage");
  expect(screen.queryByText(/Declared OAuth scopes:/)).toBeNull();
  expect(screen.getByText("Outer request description")).toBeTruthy();
  expect(screen.getByText("uuid")).toBeTruthy();
  expect(screen.getByText("Job accepted, not completed")).toBeTruthy();
  expect(screen.getByText("No response body declared.")).toBeTruthy();
  expect(screen.getByText("application/problem+json")).toBeTruthy();
  expect(screen.getByText(/External example location declared/)).toBeTruthy();
  expect(container.querySelector("script")).toBeNull();
  expect(container.querySelector('a[href^="https:"]')).toBeNull();
  expect(container.querySelectorAll(".api-complete-data[open]")).toHaveLength(
    0,
  );
  fireEvent.click(screen.getByRole("button", { name: "Copy method and path" }));
  await screen.findByText("Copied.");
  expect(writeText).toHaveBeenLastCalledWith("POST /items/{id}");
  fireEvent.click(screen.getByRole("button", { name: "Copy operation ID" }));
  expect(writeText).toHaveBeenLastCalledWith("createItem");
  const body = screen.getByRole("region", { name: "Operation request body" });
  fireEvent.click(
    within(body).getByRole("button", { name: "Copy application/json example" }),
  );
  expect(writeText).toHaveBeenLastCalledWith(supplied);
  fireEvent.click(screen.getByRole("button", { name: "Copy zero example" }));
  expect(writeText).toHaveBeenLastCalledWith("0");
  fireEvent.click(screen.getByRole("button", { name: "Copy linked example" }));
  expect(writeText).toHaveBeenLastCalledWith("<script>inert()</script>");
  fireEvent.click(
    screen.getByRole("button", {
      name: "View components/securitySchemes/bearer",
    }),
  );
  expect(onReference).toHaveBeenLastCalledWith(
    "#/components/securitySchemes/bearer",
  );
  fireEvent.click(
    screen.getByRole("button", { name: "View components/requestBodies/Input" }),
  );
  expect(onReference).toHaveBeenLastCalledWith(
    "#/components/requestBodies/Input",
  );
  const keyField = screen
    .getByText("Idempotency-Key", { selector: "strong" })
    .closest(".api-contract-field")!;
  const toggle = within(keyField as HTMLElement).getByText(
    "Complete decoded data",
  );
  fireEvent.click(toggle);
  const data = await within(keyField as HTMLElement).findByRole("group", {
    name: "JSON document",
  });
  expect(data.textContent).toContain('"$ref": "#/components/parameters/Key"');
  fireEvent.click(toggle);
  await waitFor(() =>
    expect(
      within(keyField as HTMLElement).queryByRole("group", {
        name: "JSON document",
      }),
    ).toBeNull(),
  );
  expect(onReference).toHaveBeenCalledTimes(2);
});

it("keeps missing, unsupported and malformed details separate from absent bodies", () => {
  const { container } = show(
    {},
    {
      security: [{ missing: [] }],
      parameters: [
        null,
        { name: "query", in: "query", schema: null },
        { name: "sparse", in: "cookie", content: false },
        { name: "opaque", in: "path", schema: {}, examples: [] },
      ],
      requestBody: false,
      responses: {
        default: null,
        "200": { description: "inert", content: null, headers: [] },
        "201": {
          description: "Malformed media fixture",
          content: {
            "application/json": false,
            "text/plain": { schema: { format: 42 } },
          },
        },
      },
    },
  );
  expect(
    screen.getByText(
      "Request-body definition unavailable or unsupported in this contract.",
    ),
  ).toBeTruthy();
  expect(screen.queryByText("No request body.")).toBeNull();
  expect(
    screen.getByText("Parameter or header metadata unavailable"),
  ).toBeTruthy();
  expect(
    screen.getByText(
      "Response definition unavailable or unsupported in this contract.",
    ),
  ).toBeTruthy();
  expect(screen.getByText(/Response-header metadata unavailable/)).toBeTruthy();
  expect(screen.getByText(/Named example metadata unavailable/)).toBeTruthy();
  expect(screen.getByText(/Media-type details unavailable/)).toBeTruthy();
  expect(
    screen.getAllByText(/Some schema details are unavailable/).length,
  ).toBe(2);
  expect(container.querySelectorAll(".api-complete-data[open]")).toHaveLength(
    0,
  );
});

it.each([
  {},
  { requestBody: {}, responses: {} },
  { requestBody: { content: {} }, responses: "bad" },
])("reports sparse declarations without inventing values: %j", (operation) => {
  show({}, operation);
  expect(screen.getByText("No parameters.")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Copy operation ID" }),
  ).toBeNull();
  expect(
    screen.getAllByText(/not declared|unavailable|No request body/).length,
  ).toBeGreaterThan(0);
});

it("bounds large summaries and clipboard controls while preserving complete data", async () => {
  const referenceMap = Object.fromEntries(
    Array.from({ length: 34 }, (_, i) => [
      `n${i}`,
      { $ref: `#/components/schemas/S${i}` },
    ]),
  );
  const manyExamples = Object.fromEntries(
    Array.from({ length: 34 }, (_, i) => [`e${i}`, { value: i }]),
  );
  const responses = Object.fromEntries(
    Array.from({ length: 34 }, (_, i) => [
      `${200 + i}`,
      {
        description: "Declared",
        content: { "application/json": { schema: false } },
      },
    ]),
  );
  responses["200"] = {
    description: "Declared",
    headers: referenceMap,
    content: {
      "application/json": {
        schema: false,
        examples: manyExamples,
        example: "x".repeat(65537),
      },
      ...Object.fromEntries(
        Array.from({ length: 33 }, (_, i) => [`application/example-${i}`, {}]),
      ),
    },
  } as (typeof responses)["200"];
  const { container } = show(
    {
      components: {
        securitySchemes: { bearer: { type: "http", scheme: "bearer" } },
      },
    },
    {
      security: Array.from({ length: 34 }, () => ({ bearer: [] })),
      parameters: Array.from({ length: 34 }, (_, i) => ({
        name: `p${i}`,
        in: "query",
      })),
      requestBody: {
        "x-many": referenceMap,
        content: {
          "application/json": { examples: { absent: { value: undefined } } },
        },
      },
      responses,
    },
  );
  for (const text of [
    "Additional alternatives remain",
    "Additional parameter declarations remain",
    "Additional responses remain",
    "Additional media types remain",
    "Additional headers remain",
    "Additional examples remain",
    "Additional local references remain",
  ])
    expect(screen.getAllByText(new RegExp(text)).length).toBeGreaterThan(0);
  expect(container.querySelectorAll(".api-response")).toHaveLength(32);
  expect(
    screen.getByRole("button", { name: "Copy application/json example" }),
  ).toHaveProperty("disabled", true);
  const allParameters = screen.getByText(
    "Additional parameter declarations remain in complete decoded data.",
  ).parentElement!;
  fireEvent.click(
    within(allParameters).getByText("Complete parameter declarations"),
  );
  expect(
    await within(allParameters).findByRole("group", { name: "JSON document" }),
  ).toBeTruthy();
});

it.each([
  [false, "false"],
  [0, "0"],
  [null, "null"],
  ["", ""],
  [true, "true"],
  [{ answer: 1 }, '{"answer":1}'],
  [[], "[]"],
])(
  "copies a supplied example without losing its exact decoded value: %j",
  (value, expected) => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    show(
      {},
      { requestBody: { content: { "text/plain": { example: value } } } },
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Copy text/plain example" }),
    );
    expect(writeText).toHaveBeenCalledWith(expected);
  },
);

it.each([
  ["ASCII at the limit", "x".repeat(65536), "x".repeat(65536), true],
  ["ASCII over the limit", "x".repeat(65537), "x".repeat(65537), false],
  [
    "supplementary Unicode at the limit",
    "\u{1f680}".repeat(32768),
    "\u{1f680}".repeat(32768),
    true,
  ],
  [
    "supplementary Unicode over the limit",
    "\u{1f680}".repeat(32768) + "x",
    "\u{1f680}".repeat(32768) + "x",
    false,
  ],
  [
    "encoded JSON at the limit",
    { text: "x".repeat(65525) },
    '{"text":"' + "x".repeat(65525) + '"}',
    true,
  ],
  [
    "encoded JSON over the limit",
    { text: "x".repeat(65526) },
    '{"text":"' + "x".repeat(65526) + '"}',
    false,
  ],
] as const)(
  "uses the documented UTF-16 clipboard boundary: %s",
  async (_, value, expected, allowed) => {
    // Expected limits and literal JSON are independent of the production helper.
    expect(expected.length).toBe(allowed ? 65536 : 65537);
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    show(
      {},
      { requestBody: { content: { "application/json": { example: value } } } },
    );
    const button = screen.getByRole("button", {
      name: "Copy application/json example",
    });
    expect(button).toHaveProperty("disabled", !allowed);
    fireEvent.click(button);
    if (allowed) {
      await screen.findByText("Copied.");
      expect(writeText).toHaveBeenCalledExactlyOnceWith(expected);
      expect(
        screen.queryByText(/Example too large for clipboard controls/),
      ).toBeNull();
    } else {
      expect(writeText).not.toHaveBeenCalled();
      expect(
        screen.getByText(/Example too large for clipboard controls/),
      ).toBeTruthy();
      expect(screen.queryByText("Copied.")).toBeNull();
    }
  },
);

it("keeps literal placeholders available after clipboard refusal and retries only when asked", async () => {
  const writeText = vi
    .fn()
    .mockRejectedValueOnce(new Error("private clipboard details"))
    .mockResolvedValueOnce(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const { onReference } = show({}, { operationId: "createItem" });
  const button = screen.getByRole("button", { name: "Copy method and path" });
  fireEvent.click(button);
  expect(await screen.findByRole("status")).toHaveProperty(
    "textContent",
    "Copy unavailable. Select the reference and copy it manually.",
  );
  expect(screen.getByText("POST /items/{id}")).toBeTruthy();
  expect(screen.queryByText("Copied.")).toBeNull();
  expect(screen.queryByText("private clipboard details")).toBeNull();
  expect(writeText).toHaveBeenCalledExactlyOnceWith("POST /items/{id}");
  fireEvent.click(button);
  expect(await screen.findByText("Copied.")).toBeTruthy();
  expect(writeText).toHaveBeenCalledTimes(2);
  expect(writeText).toHaveBeenNthCalledWith(2, "POST /items/{id}");
  expect(onReference).not.toHaveBeenCalled();
});

it("retains unavailable declarations and summarizes OAuth conjunctions within display limits", () => {
  const first = show({}, { security: undefined });
  expect(
    screen.getByText(
      "Authentication details unavailable. Do not assume anonymous access.",
    ),
  ).toBeTruthy();
  expect(
    screen.getByText("Definition unavailable in this contract."),
  ).toBeTruthy();
  first.unmount();
  const schemes = Object.fromEntries(
    Array.from({ length: 34 }, (_, i) => [
      `s${i}`,
      { type: "oauth2", flows: {} },
    ]),
  );
  const requirement = Object.fromEntries(
    Object.keys(schemes).map((name) => [name, ["read"]]),
  );
  show(
    { components: { securitySchemes: schemes } },
    { security: [requirement] },
  );
  expect(
    screen.getByText("Additional schemes remain in complete decoded data."),
  ).toBeTruthy();
  expect(screen.getAllByText(/Declared OAuth scopes/)).toHaveLength(32);
});

it("keeps schema examples as annotations with explicit limits and malformed fallback", () => {
  show(
    {},
    {
      parameters: [
        {
          name: "annotations",
          in: "query",
          schema: {
            example: false,
            examples: Array.from({ length: 33 }, (_, i) => i),
          },
        },
        { name: "invalid", in: "query", schema: { examples: {} } },
      ],
    },
  );
  expect(
    screen.getByText(
      "Additional schema examples remain in complete decoded data.",
    ),
  ).toBeTruthy();
  expect(
    screen.getByText(
      "Schema example metadata unavailable; inspect complete data.",
    ),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Copy schema example" }),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Copy schema example 32" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Copy schema example 33" }),
  ).toBeNull();
});
