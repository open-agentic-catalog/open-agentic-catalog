// Known native kinds reuse the Catalog's labels, colors and decorative glyphs.
import type { Kind } from "../api/client";
import { kindLabel } from "../resource-kinds";
import { controlIcons } from "../shell/Icon";

export function KindBadge({ kind }: { kind: Kind }) {
  return (
    <span className={`badge catalog-kind-badge ${kind}`}>
      {controlIcons[kind]}
      {kindLabel(kind)}
    </span>
  );
}
