// Synchronize an owned fixture boundary without hiding an earlier operation failure.
export async function fixtureWait(event, label, operation, timeoutMs = 3000) {
  let timer;
  try {
    const waiting = [
      event,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`Owned fixture ${label} timed out`)),
          timeoutMs,
        );
      }),
    ];
    if (operation)
      waiting.push(
        operation.then(
          () => {
            throw new Error(`Owned fixture ${label} finished before readiness`);
          },
          () => {
            throw new Error(`Owned fixture ${label} failed before readiness`);
          },
        ),
      );
    return await Promise.race(waiting);
  } finally {
    clearTimeout(timer);
  }
}

export async function fixturePoll(predicate, label, timeoutMs = 3000) {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = await predicate();
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`Owned fixture ${label} timed out`);
}
