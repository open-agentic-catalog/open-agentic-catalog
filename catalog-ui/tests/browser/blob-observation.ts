// Observe native attachment lifetimes without replacing browser URLs or image loading.
import type { Page } from "@playwright/test";

type Observation = {
  document: string;
  created: { url: string; type: string }[];
  revoked: string[];
};
type ObservedWindow = Window & { attachmentObservation?: Observation };
export async function observeAttachments(page: Page) {
  await page.addInitScript(() => {
    const state: Observation = {
      document: crypto.randomUUID(),
      created: [],
      revoked: [],
    };
    (window as ObservedWindow).attachmentObservation = state;
    const create = URL.createObjectURL.bind(URL);
    const revoke = URL.revokeObjectURL.bind(URL);
    URL.createObjectURL = (value) => {
      const url = create(value);
      state.created.push({
        url,
        type: value instanceof Blob ? value.type : "",
      });
      return url;
    };
    URL.revokeObjectURL = (url) => {
      state.revoked.push(url);
      revoke(url);
    };
  });
}
export async function attachments(page: Page) {
  return page.evaluate(() => {
    const state = (window as ObservedWindow).attachmentObservation;
    if (!state)
      throw new Error("Native attachment observation was not installed");
    return structuredClone(state);
  });
}
