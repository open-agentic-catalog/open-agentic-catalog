import type { Locator, Page } from "@playwright/test";

/** Open native disclosure ancestors through their summaries, as a reader would. */
export async function revealControl(target: Locator) {
  await target.waitFor({ state: "attached" });
  // A closed detail still exposes its own summary (including text/span children).
  // Reveal only details that hide the target; let the caller activate that summary.
  const inOwnSummary = await target.evaluate((element) => {
    const summary = element.closest("summary");
    return !!summary && summary.parentElement === element.closest("details");
  });
  const parents = target.locator(
    inOwnSummary
      ? "xpath=ancestor::details[position() > 1]"
      : "xpath=ancestor::details",
  );
  for (let index = 0; index < (await parents.count()); index++) {
    const parent = parents.nth(index);
    if ((await parent.getAttribute("open")) === null)
      await parent.locator(":scope > summary").click();
  }
}

export async function openDisclosure(page: Page, label: string) {
  const summary = page
    .locator("summary")
    .filter({
      hasText: new RegExp(
        `^\\s*${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*$`,
      ),
    })
    .first();
  await summary.waitFor({ state: "attached" });
  await revealControl(summary);
  const parent = summary.locator("..");
  if ((await parent.getAttribute("open")) === null) await summary.click();
}
