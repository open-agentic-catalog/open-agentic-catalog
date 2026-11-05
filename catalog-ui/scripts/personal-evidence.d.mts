export function personalCommandOutcome(input: {
  status: number;
  method: string;
  problem: unknown;
  requestId?: string | undefined;
}): {
  operation: "personal.save" | "personal.update" | "personal.remove";
  status: number;
  code: string | undefined;
  refusal: "transition_time_regression" | "unclassified";
  requestId: string | undefined;
};
