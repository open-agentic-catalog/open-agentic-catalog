import { expect, it } from "vitest";
import { reusableJSON } from "./draft-reuse";
import { DocumentInputError, InputError } from "./input-validation";
import {
  newRepresentation,
  parseObject,
  representationInput,
} from "./representations";

const precisionMessage =
  "The browser cannot preserve every number in this JSON. Use values it can represent exactly.";

it.each([
  ["initial input", parseObject],
  ["draft reuse", reusableJSON],
] as const)(
  "%s rejects decimal rounding and underflow with a safe document error",
  (_name, parse) => {
    for (const token of [
      "0.123456789012345678901",
      "1.0000000000000001",
      "0.100000000000000005",
      "1e-9999",
      "-1e-9999",
    ]) {
      const text = `{"extension":{"values":[${token}]}}`;
      expect(() => parse(text)).toThrow(DocumentInputError);
      expect(() => parse(text)).toThrow(precisionMessage);
    }
  },
);

it.each([
  ["initial input", parseObject],
  ["draft reuse", reusableJSON],
] as const)(
  "%s preserves transport-safe decimals and numeric-looking strings",
  (_name, parse) => {
    expect(
      parse(
        '{"values":[1.25,1e-3,12.500e-1,0e-9999,0.0000e9999,5e-324],"0.123456789012345678901":"1e-9999"}',
      ),
    ).toEqual({
      values: [1.25, 0.001, 1.25, 0, 0, 5e-324],
      "0.123456789012345678901": "1e-9999",
    });
    expect(parse('{"values":[-0,1.0,9007199254740991]}')).toEqual({
      values: [-0, 1, Number.MAX_SAFE_INTEGER],
    });
  },
);

it("keeps the existing unsafe-integer refusal and attributes precision errors to the raw editor", () => {
  expect(() => parseObject('{"number":9007199254740992}')).toThrow(
    "Whole numbers must be between -9007199254740991 and 9007199254740991.",
  );
  const draft = {
    ...newRepresentation("agent"),
    content: '{"extension":0.123456789012345678901}',
  };
  expect(() => representationInput(draft)).toThrow(InputError);
  expect(() => representationInput(draft)).toThrow(precisionMessage);
  try {
    representationInput(draft);
  } catch (error) {
    expect(error).toHaveProperty("fieldId", `${draft.editorID}-content`);
  }
  expect(draft.content).toBe('{"extension":0.123456789012345678901}');
});
