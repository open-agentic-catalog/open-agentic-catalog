// Container-readable inputs stay beneath the supervisor's private parent.
import { chmod, mkdir, writeFile } from "node:fs/promises";

export async function writeFixtureInput(path, data) {
  await writeFile(path, data, { mode: 0o444 });
  await chmod(path, 0o444);
}

// Only a fresh writable child is shared with container UID 65532. The
// supervisor's parent remains private; CLI receipts and locks stay 0600.
export async function createFixtureStateDirectory(path) {
  await mkdir(path, { mode: 0o777 });
  await chmod(path, 0o777);
}
