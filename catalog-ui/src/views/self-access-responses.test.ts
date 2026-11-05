import { expect, it } from "vitest";
import { effective } from "./access-responses";
import { assignment, role } from "../../tests/unit/policy-fixtures";

const direct = { assignment, role };
const grouped = {
  role,
  assignment: { ...assignment, membershipId: undefined, groupId: "group-a" },
  groupName: "Current readable team",
};

it("accepts exact direct/group paths without treating a caption as directory authority", () => {
  expect(effective(direct)).toBe(true);
  expect(effective(grouped)).toBe(true);
  expect(effective({ ...grouped, groupName: undefined })).toBe(true);
  expect(effective({ ...grouped, groupName: "😀".repeat(120) })).toBe(true);
  expect(effective({ ...grouped, groupName: "<script>literal</script>" })).toBe(
    true,
  );
});

it.each([
  null,
  { ...direct, role: null },
  { ...direct, assignment: null },
  { ...direct, role: { ...role, id: "other-role" } },
  { ...direct, role: { ...role, tenantId: "other-tenant" } },
  { ...direct, groupName: "Unrelated group" },
  { ...grouped, groupName: null },
  { ...grouped, groupName: 1 },
  { ...grouped, groupName: "" },
  { ...grouped, groupName: "   " },
  { ...grouped, groupName: "😀".repeat(121) },
])("rejects a malformed or wrongly bound self path: %#", (value) => {
  expect(effective(value)).toBe(false);
});
