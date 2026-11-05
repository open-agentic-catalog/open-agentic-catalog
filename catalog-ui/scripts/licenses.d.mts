import type { Plugin } from "vite";
export function legalAssets(root: string): Plugin;
export function collectLicenses(root: string, output: string): Promise<void>;
