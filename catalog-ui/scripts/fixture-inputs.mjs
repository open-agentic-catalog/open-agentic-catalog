// Container-readable inputs stay beneath the supervisor's private parent.
import { chmod, writeFile } from "node:fs/promises";

export async function writeFixtureInput(path, data) {
  await writeFile(path, data, { mode: 0o444 });
  await chmod(path, 0o444);
}
