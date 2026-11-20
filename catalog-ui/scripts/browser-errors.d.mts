import type { BrowserContext } from "@playwright/test";
export function browserErrors(): {
  observe(context: BrowserContext, resourceOrigins: string[]): () => void;
  results(): { category: string; count: number }[];
};
