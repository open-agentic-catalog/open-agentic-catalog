import type { Page, Response, Locator } from "@playwright/test";
export function authorizationJourney(options: {
  page: Page;
  memberPage: Page;
  origin: string;
  basePath?: string;
  memberAlias: string;
  captureDocumentation?: (name: string, target: Locator) => Promise<void>;
  expectedCatalogCount?: number;
  inspectCommandOutcome?:
    | ((outcome: { title: string; response: Response }) => Promise<void>)
    | undefined;
  expectFailure: (method: string, path: string, status: number) => void;
  qualifyRetainedPolicy?:
    | ((policy: {
        roleId: string;
        groupId: string;
        assignmentId: string;
        membershipId: string;
      }) => Promise<void>)
    | undefined;
}): Promise<{
  roleId: string;
  groupId: string;
  assignmentId: string;
  membershipId: string;
}>;
