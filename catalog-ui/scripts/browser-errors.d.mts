import type { BrowserContext } from "@playwright/test";
export function browserErrors(
  onFirst?: (event: {
    category: string;
    route: string;
    ownedOrigin: boolean;
  }) => void,
): {
  observe(context: BrowserContext, resourceOrigins: string[]): () => void;
  results(): { category: string; count: number }[];
};
