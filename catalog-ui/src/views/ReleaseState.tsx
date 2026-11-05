// A release label describes its disposition, not trust or snapshot availability.
import type { components } from "../api/schema";

export function ReleaseState({
  state,
}: {
  state: components["schemas"]["ListingRelease"]["state"];
}) {
  const tones = {
    published: "success",
    deprecated: "warning",
    retired: "neutral",
    superseded: "neutral",
    revoked: "danger",
  };
  return (
    <span className="state-badge" data-tone={tones[state]}>
      {state}
    </span>
  );
}
