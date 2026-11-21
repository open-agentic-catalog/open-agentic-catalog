// The authorization inventory independently tracks every operation in Control OpenAPI.
import inventory from "../../../test/authorization/inventory.json" with { type: "json" };

export function operationCountLabel(visible = inventory.operations.length) {
  return `${visible} of ${inventory.operations.length} operations`;
}
