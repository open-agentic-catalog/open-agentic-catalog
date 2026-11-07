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
