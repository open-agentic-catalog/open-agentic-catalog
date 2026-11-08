// Reuse native test events and the shared finite required-file completion policy.
import { resolve } from "node:path";
import { runRequiredTests } from "../../../catalog-ui/scripts/native-test-completion.mjs";

const [profile, source, ...files] = process.argv.slice(2);
if (!["unit", "functional"].includes(profile) || !source || !files.length)
  throw new Error("Select SDK profile, source and required test files");
await runRequiredTests(files, {
  timeout: 30000,
  coverage: profile === "unit",
  coverageIncludeGlobs: [resolve(source)],
});
