// Fixed section anchors keep long records navigable without storing documents or search criteria.
import { useEffect } from "react";

export const localRecordSections = [
  { id: "record-overview", label: "Overview" },
  { id: "record-assessments", label: "Validation and assessments" },
  { id: "record-handoff", label: "Use this resource" },
  { id: "record-evidence", label: "Technical details and evidence" },
] as const;
export const externalRecordSections = [
  { id: "record-overview", label: "Overview" },
  { id: "record-provenance", label: "Source provenance" },
  { id: "record-handoff", label: "Use this resource" },
  { id: "record-evidence", label: "Technical details and evidence" },
] as const;

function focusSection(id: string) {
  const target = document.getElementById(id);
  if (!target) return;
  target.focus({ preventScroll: true });
  target.scrollIntoView({ block: "start" });
}

/** Native fragments preserve ordinary Back/Forward; only known mounted headings receive focus. */
export function RecordNavigation({
  sections,
}: {
  sections: readonly { id: string; label: string }[];
}) {
  useEffect(() => {
    const arrive = () => {
      const target = sections.find(
        (section) => location.hash === `#${section.id}`,
      );
      if (target) focusSection(target.id);
    };
    arrive();
    window.addEventListener("hashchange", arrive);
    return () => window.removeEventListener("hashchange", arrive);
  }, [sections]);
  return (
    <nav className="record-links record-sections" aria-label="Record sections">
      {sections.map(({ id, label }) => (
        <a key={id} href={`#${id}`} onClick={() => focusSection(id)}>
          {label}
        </a>
      ))}
    </nav>
  );
}
