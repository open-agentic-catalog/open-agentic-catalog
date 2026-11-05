// The browser consumes only this entry point from the native fixture module.
import type { Catalog } from "../tests/browser/fixtures";
export function startCatalog(
  buildDirectory: string,
  root: string,
  basePath: string,
  options?: {
    webhooks?: boolean;
    publisherVerification?: boolean;
    demoArchives?: boolean;
    authoredPlugin?: boolean;
    artwork?: boolean;
    curated?: boolean;
    publicationAudiences?: boolean;
    maintainedIdentities?: boolean;
    serviceActivity?: boolean;
    performanceScale?: string;
  },
): Promise<Catalog>;
