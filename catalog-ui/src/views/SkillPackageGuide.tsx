/** Package preparation stays in the lazy Skill authoring payload. */
export default function SkillPackageGuide({
  completePackage,
  onPrepareDiscovery,
}: {
  completePackage: boolean;
  onPrepareDiscovery?: (() => void) | undefined;
}) {
  return (
    <div className="skill-package-choice">
      <p>
        Write/import SKILL.md for a standalone document. Upload a complete Skill
        package when the instructions need scripts, references or assets.
      </p>
      <details>
        <summary>Skill package structure and limits</summary>
        <pre>{`skill-name/
  SKILL.md
  scripts/       (optional)
  references/    (optional)
  assets/        (optional)`}</pre>
        <p>
          The root directory must match the name in SKILL.md. A standalone
          document cannot confirm that directory name.
        </p>
        <p>
          Use ZIP or gzipped tar. The archive limit is 16 MiB, with up to 1024
          files and 64 MiB after expansion. Each file is limited to 16 MiB,
          paths to 16 levels below the root, and expansion to 100 times the
          archive size.
        </p>
        <p>
          The deployment request limit also applies to the base64-encoded
          archive and JSON request. It can reduce the upload limit.
        </p>
      </details>
      {completePackage && (
        <>
          <p>
            Choose a non-empty archive up to 16 MiB. The deployment request
            limit may be smaller. Archive bytes are inspected, not stored or
            executed.
          </p>
          <p>
            You can release a package-only Skill and read its authenticated
            Catalog record. A discovery snapshot also needs a supported primary
            document or delivery representation. Keep the complete package in a
            non-primary row. A Git/OCI handoff alone does not supply discovery
            content.
          </p>
        </>
      )}
      {onPrepareDiscovery && (
        <button type="button" onClick={onPrepareDiscovery}>
          Add primary SKILL.md for discovery
        </button>
      )}
    </div>
  );
}
