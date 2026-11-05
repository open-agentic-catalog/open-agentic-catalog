import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
export async function developmentHost(
  launchArgs,
  editorEnv,
  resultFile,
  timeout,
) {
  const editor = spawn(editorEnv.CATALOG_VSCODE_HOST, launchArgs, {
    env: editorEnv,
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let diagnostics = "";
  for (const stream of [editor.stdout, editor.stderr])
    stream.on("data", (chunk) => {
      diagnostics = (diagnostics + chunk.toString()).slice(-16000);
    });
  const exited = new Promise((resolve) => editor.on("exit", resolve));
  const progress = async () => {
    let output = "";
    for (const name of [
      "plugin",
      "connection",
      "inspection",
      "consumer",
      "local-tools",
      "capabilities",
      "runners",
    ]) {
      try {
        output +=
          `\n${name}:\n` +
          (await readFile(
            join(editorEnv.CATALOG_VSCODE_DEBUG_DIR, name + "-progress.log"),
            "utf8",
          ));
      } catch {
        /* A failed earlier phase need not create this owned log. */
      }
    }
    return output;
  };
  const stopEditor = async () => {
    await Promise.race([
      exited,
      new Promise((resolve) => setTimeout(resolve, 5000)),
    ]);
    for (const signal of ["SIGTERM", "SIGKILL"]) {
      try {
        process.kill(-editor.pid, signal);
      } catch (error) {
        if (error.code !== "ESRCH") throw error;
      }
      await Promise.race([
        exited,
        new Promise((resolve) => setTimeout(resolve, 1000)),
      ]);
    }
  };
  try {
    const deadline = Date.now() + timeout;
    let result;
    while (Date.now() < deadline) {
      try {
        result = JSON.parse(await readFile(resultFile, "utf8"));
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 100));
      }
    }
    if (!result) {
      throw new Error(
        `Editor qualification deadline: ${await progress()}\n${diagnostics}`,
      );
    }
    if (!result.passed)
      throw new Error(
        result.error +
          (await progress()) +
          "\nOwned editor diagnostics:\n" +
          diagnostics,
      );
  } finally {
    await stopEditor();
  }
}
