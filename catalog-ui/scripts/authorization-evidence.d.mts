type CommandInput = {
  method: unknown;
  path: unknown;
  status: unknown;
  problem: unknown;
};

export function authorizationCommandOutcome(input: CommandInput): {
  operation: string;
  method: string;
  status: number | undefined;
  code: string;
  refusal: string;
};

export function fixtureSetupError(input: CommandInput): Error;

export function roleRefusalReadback(input: {
  status: unknown;
  document: unknown;
  etag: unknown;
  date: unknown;
}): {
  readSucceeded: boolean;
  roleState: "active" | "archived" | "unknown";
  revisionMatchesExpected: boolean | undefined;
  responseClockBeforeUpdate: boolean | undefined;
};
