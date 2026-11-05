// Type the existing native Node build-plugin boundary used by vite.config.ts.
import type { Plugin } from "vite";

export function coverageSourceFiles(ui: string): string[];
export function coveragePlugin(
  directory: string | undefined,
  ui?: string,
): Plugin[];
