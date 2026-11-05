// Bound scripted provider replies before Pi can dispatch their tool calls.
export const activeTools = ["ard_catalog", "ard_install", "ard_search"];
export function boundedResponses(steps, maxCalls = 20, maxPages = 3) {
  if (
    !Array.isArray(steps) ||
    steps.length > 41 ||
    !Number.isInteger(maxCalls) ||
    maxCalls < 1 ||
    maxCalls > 20 ||
    maxPages !== 3
  )
    throw new Error("invalid Pi script bounds");
  let calls = 0;
  let pages = 0;
  return steps.map((step) => async (...args) => {
    const value = typeof step === "function" ? await step(...args) : step;
    if (!value || !Array.isArray(value.content))
      throw new Error("invalid Pi scripted response");
    for (const block of value.content) {
      if (block.type !== "toolCall") continue;
      if (!activeTools.includes(block.name) || ++calls > maxCalls)
        throw new Error("Pi tool budget exceeded");
      if (block.name === "ard_search") {
        pages = block.arguments?.pageToken ? pages + 1 : 1;
        if (pages > maxPages) throw new Error("Pi page budget exceeded");
      }
    }
    return value;
  });
}
