// Row identity belongs to the editor lifetime, never to its serialized native data.
import { useRef } from "react";

/** Keep native editor rows stable through edits; callers remove tokens with rows. */
export function useEditorRowKeys(generation: number | string = 0) {
  const rows = useRef({ generation, keys: [] as number[], next: 0 });
  if (rows.current.generation !== generation)
    rows.current = { generation, keys: [], next: rows.current.next };
  return {
    key(index: number) {
      while (rows.current.keys.length <= index)
        rows.current.keys.push(rows.current.next++);
      return rows.current.keys[index]!;
    },
    remove(index: number) {
      rows.current.keys.splice(index, 1);
    },
  };
}

/** Focus a surviving row's first input before deleting the focused Remove button. */
export function useRemovableInputRows() {
  const keys = useEditorRowKeys();
  const elements = useRef<(HTMLElement | null)[]>([]);
  const add = useRef<HTMLButtonElement>(null);
  return {
    key: keys.key,
    add,
    rowRef: (index: number) => (element: HTMLElement | null) => {
      elements.current[index] = element;
    },
    remove(index: number) {
      const neighbor =
        elements.current[index + 1] ?? elements.current[index - 1];
      (neighbor?.querySelector("input") ?? add.current)?.focus();
      keys.remove(index);
    },
  };
}
