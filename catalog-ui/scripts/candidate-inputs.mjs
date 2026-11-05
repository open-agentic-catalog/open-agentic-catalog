// Explicit retained image selection for ordinary packaged journeys only.
import { execFileSync } from "node:child_process";

export function candidateArguments(environment, modes) {
  const backend = environment.CATALOG_CANDIDATE_BACKEND;
  const ui = environment.CATALOG_CANDIDATE_UI;
  if (!backend && !ui) return null;
  if (!backend || !ui || Object.values(modes).some(Boolean))
    throw new Error(
      "Candidates require both images and an ordinary packaged journey",
    );
  return [
    "-B",
    "test/ci/candidate.py",
    "admit",
    "--backend",
    backend,
    "--ui",
    ui,
  ];
}

export function loadCandidates(root, environment, modes) {
  const args = candidateArguments(environment, modes);
  if (!args) return null;
  return JSON.parse(
    execFileSync("python3", args, {
      cwd: root,
      timeout: 60000,
      maxBuffer: 4 * 1024 * 1024,
      encoding: "utf8",
    }),
  );
}
