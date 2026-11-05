export const journeys: string[];
export function performancePolicy(
  value?: string,
): Record<
  | "scale"
  | "assertion"
  | "test"
  | "suite"
  | "phase"
  | "readiness"
  | "probe"
  | "ownership"
  | "seed"
  | "admin",
  number
>;
