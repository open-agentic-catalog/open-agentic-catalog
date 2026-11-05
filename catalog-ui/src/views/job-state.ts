// Job filters and lifecycle explanations use only retained public metadata.
export const jobStates = {
  queued: "Queued; waiting to be claimed by a compatible worker.",
  running:
    "Running under a worker lease. Reported progress is not a committed result.",
  waiting:
    "Waiting after a retryable failure. Not before is the earliest eligible time, not a promised retry time.",
  succeeded:
    "Completed successfully. Recorded results do not prove the current target or index is still active.",
  failed:
    "Finished with a failure. An eligible manual retry creates a separate child Job.",
  cancelled:
    "Cancelled. Side effects committed before cancellation are not undone.",
  dead_letter:
    "Automatic retries are exhausted. Inspect the failure and restore the dependency or policy before considering an eligible manual retry.",
};

export type JobFilters = { kind: string; state: string; parentJobId: string };

/** Preserve applied exact filters in the current route and browser history. */
export function jobFilters(search: string): JobFilters {
  const query = new URLSearchParams(search);
  const exact = (name: string) => {
    const values = query.getAll(name);
    return values.length > 1 ? "unsupported" : (values[0] ?? "");
  };
  return {
    kind: exact("kind"),
    state: exact("state"),
    parentJobId: exact("parentJobId"),
  };
}

/** Omit empty optional filters; literal kinds and exact parent IDs remain explicit. */
export function jobQueueURL(basePath: string, filters: JobFilters) {
  const query = new URLSearchParams();
  for (const [name, value] of Object.entries(filters))
    if (value) query.set(name, value);
  return `${basePath}/jobs${query.size ? `?${query}` : ""}`;
}
