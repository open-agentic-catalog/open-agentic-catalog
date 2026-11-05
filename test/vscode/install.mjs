import { downloadAndUnzipVSCode } from "../../extensions/vscode/node_modules/@vscode/test-electron/out/index.js";
import { resolve } from "node:path";
const executable = await downloadAndUnzipVSCode({
  version: "1.141.0",
  cachePath: resolve(import.meta.dirname, ".tools/host"),
});
console.log(`CATALOG_VSCODE_HOST=${executable}`);
