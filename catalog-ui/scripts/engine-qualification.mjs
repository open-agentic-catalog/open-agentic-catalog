import { readFile } from "node:fs/promises";
import { join } from "node:path";

// Only portable tasks with independent behavioral assertions are selected.
export const engineJourneys = [
  "search pages contain disjoint real releases and submitting filters resets the cursor",
  "metadata-only Catalog readers gain content and history links only with their independent current grants",
  "subpath: resume authoring after uncertain Listing revision without repeating acknowledged writes",
  "corrupt retained documents never become downloads and real bytes recover with replaced object URLs",
  "root absent version and opinion: indexed detail retains search and guards drafts through Back/Forward",
];

export function selectedEngine(args) {
  if (!args.includes("--engine")) return;
  if (
    args.length !== 2 ||
    args[0] !== "--engine" ||
    !["firefox", "webkit"].includes(args[1])
  )
    throw new Error("Select exactly --engine firefox or --engine webkit");
  return args[1];
}

// The runner, browser launcher and binary manifest come from separate packages.
export async function lockedPlaywrightVersion(ui) {
  const lock = JSON.parse(await readFile(join(ui, "package-lock.json")));
  for (const name of ["@playwright/test", "playwright", "playwright-core"]) {
    const path = "node_modules/" + name;
    const installed = JSON.parse(
      await readFile(join(ui, path, "package.json")),
    );
    const expected = lock.packages[path]?.version;
    if (!expected || installed.version !== expected)
      throw new Error(
        name + " differs from the UI lockfile. Run make ui-install",
      );
  }
  return lock.packages["node_modules/@playwright/test"].version;
}

export function journeyResults(tests) {
  return engineJourneys.map((title) => {
    const matches = tests.filter((test) => test.title === title);
    if (!matches.length) return { title, status: "missing" };
    const [test] = matches;
    const status =
      matches.length === 1 && test.results.length === 1
        ? test.results[0].status
        : "failed";
    return {
      title,
      status:
        test.expectedStatus !== "passed" && status === "passed"
          ? "failed"
          : status,
    };
  });
}

// A truncated or stale partial report must never qualify a browser.
export function completeEngineReport(report) {
  return (
    ["firefox", "webkit"].includes(report.engine) &&
    report.platform === "linux" &&
    report.architecture === "x64" &&
    !!report.os &&
    !!report.kernel &&
    !!report.playwright &&
    /^[a-f0-9]{40}$/.test(report.source?.commit) &&
    /^[a-f0-9]{64}$/.test(report.source?.sha256) &&
    /^[a-f0-9]{64}$/.test(report.assetsSHA256) &&
    /^\d+$/.test(report.binary?.revision) &&
    report.cleanup === "passed" &&
    report.oidc?.status === "passed" &&
    report.oidc.cleanup === "passed" &&
    report.oidc.engine === report.engine &&
    typeof report.binary?.version === "string" &&
    report.oidc.browser === report.binary.version &&
    Array.isArray(report.oidc.images) &&
    report.oidc.images.length === 2 &&
    report.oidc.images.every((id) => /^sha256:[0-9a-f]{64}$/.test(id)) &&
    Array.isArray(report.journeys) &&
    report.journeys.length === engineJourneys.length &&
    engineJourneys.every(
      (title, index) =>
        report.journeys[index]?.title === title &&
        report.journeys[index].status === "passed",
    )
  );
}
