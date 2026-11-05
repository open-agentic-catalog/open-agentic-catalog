import { fireEvent } from "@testing-library/react";

/** Follow the native disclosure before interacting with an intentionally secondary editor. */
export function revealEditor(element: Element) {
  const ancestors: HTMLDetailsElement[] = [];
  for (
    let parent = element.parentElement;
    parent;
    parent = parent.parentElement
  )
    if (parent instanceof HTMLDetailsElement) ancestors.unshift(parent);
  for (const details of ancestors) {
    if (!details.open) {
      const summary = details.querySelector(":scope > summary");
      if (summary) fireEvent.click(summary);
    }
  }
}
