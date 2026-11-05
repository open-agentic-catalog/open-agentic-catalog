package adapter

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type testArchiveEntry struct {
	name     string
	content  []byte
	mode     os.FileMode
	typeflag byte
	linkname string
	nonUTF8  bool
}

func TestSkillPackageZIPAndGZIPInspection(t *testing.T) {
	t.Parallel()
	skill := []byte("---\nname: reconcile-invoices\ndescription: Reconcile disputed invoices\n---\n# Workflow\n")
	sentinel := filepath.Join(t.TempDir(), "must-not-exist")
	script := []byte("#!/bin/sh\ntouch " + sentinel + "\n")
	entries := []testArchiveEntry{
		{name: "reconcile-invoices/", mode: os.ModeDir | 0o755, typeflag: tar.TypeDir},
		{name: "reconcile-invoices/scripts/", mode: os.ModeDir | 0o755, typeflag: tar.TypeDir},
		{name: "reconcile-invoices/SKILL.md", content: skill, mode: 0o644},
		{name: "reconcile-invoices/scripts/run.sh", content: script, mode: 0o755},
		{name: "reconcile-invoices/assets/empty.txt", content: nil, mode: 0o644},
	}
	for _, test := range []struct {
		name      string
		mediaType string
		archive   []byte
	}{
		{name: "zip", mediaType: MediaSkillZIP, archive: makeZIP(t, entries)},
		{name: "gzip", mediaType: MediaSkillGZIP, archive: makeGZIP(t, entries)},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{
				MediaType:   test.mediaType,
				PackageData: test.archive,
				Handoff: &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{
					Repository: "https://git.example.com/skills/catalog.git",
					Commit:     strings.Repeat("a", 40),
					Directory:  "skills/reconcile-invoices",
				}},
			})
			if hasErrors(result.Findings) || result.Package == nil || result.Handoff == nil {
				t.Fatalf("valid package returned %#v and %#v", result.Package, result.Findings)
			}
			if result.Package.StructureState != catalog.PackageAssessmentPassed || result.Package.RootDirectory != "reconcile-invoices" || result.Package.FileCount != 3 || result.Package.ExpandedSize != int64(len(skill)+len(script)) {
				t.Fatalf("inspection = %#v", result.Package)
			}
			if result.NormalizedFacts["name"] != "reconcile-invoices" || result.NormalizedFacts["description"] != "Reconcile disputed invoices" || !hasFinding(result.Findings, "SKILL_PACKAGE_ASSESSMENTS_NOT_ASSESSED") {
				t.Fatalf("package facts/findings = %#v / %#v", result.NormalizedFacts, result.Findings)
			}
			for _, state := range []catalog.PackageAssessmentState{result.Package.Assessments.Malware, result.Package.Assessments.Secrets, result.Package.Assessments.License, result.Package.Assessments.SBOM, result.Package.Assessments.Provenance} {
				if state != catalog.PackageAssessmentNotAssessed {
					t.Fatalf("optional assessment state = %q", state)
				}
			}
			for index := 1; index < len(result.Package.Files); index++ {
				if result.Package.Files[index-1].Path >= result.Package.Files[index].Path {
					t.Fatal("file inventory is not in canonical path order")
				}
			}
			var scriptFile catalog.SkillPackageFile
			for _, file := range result.Package.Files {
				if file.Path == "reconcile-invoices/scripts/run.sh" {
					scriptFile = file
				}
			}
			wantDigest := sha256.Sum256(script)
			if !scriptFile.Executable || scriptFile.MediaType != "application/x-sh" || scriptFile.Digest != fmt.Sprintf("sha256:%x", wantDigest) {
				t.Fatalf("script inventory = %#v", scriptFile)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("package script was executed or sentinel check failed: %v", err)
			}
		})
	}
}

func TestSkillPackageRejectsUnsafeZIPEntries(t *testing.T) {
	t.Parallel()
	skill := validPackagedSkill("safe-skill")
	tests := []struct {
		name    string
		entries []testArchiveEntry
		mutate  func([]byte) []byte
		limits  skillPackageLimits
		code    string
	}{
		{name: "traversal", entries: []testArchiveEntry{{name: "../safe-skill/SKILL.md", content: skill}}, code: "SKILL_PACKAGE_PATH_INVALID"},
		{name: "absolute", entries: []testArchiveEntry{{name: "/safe-skill/SKILL.md", content: skill}}, code: "SKILL_PACKAGE_PATH_INVALID"},
		{name: "backslash", entries: []testArchiveEntry{{name: `safe-skill\SKILL.md`, content: skill}}, code: "SKILL_PACKAGE_PATH_INVALID"},
		{name: "non UTF-8 flag", entries: []testArchiveEntry{{name: "safe-skill/\xff", content: skill, nonUTF8: true}}, code: "SKILL_PACKAGE_PATH_INVALID"},
		{name: "internal traversal", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/a/../other.md", content: skill}}, code: "SKILL_PACKAGE_PATH_INVALID"},
		{name: "duplicate", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/SKILL.md", content: skill}}, code: "SKILL_PACKAGE_PATH_DUPLICATE"},
		{name: "case collision", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/Notes.txt", content: []byte("one")}, {name: "safe-skill/notes.txt", content: []byte("two")}}, code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "file used as parent", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/references", content: []byte("file")}, {name: "safe-skill/references/README.md", content: []byte("child")}}, code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "file replaces implicit parent", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/references/README.md", content: []byte("child")}, {name: "safe-skill/references", content: []byte("file")}}, code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "case-conflicting implicit parent", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/References/one.md", content: []byte("one")}, {name: "safe-skill/references/two.md", content: []byte("two")}}, code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "explicit directory conflicts with implicit parent case", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/References/one.md", content: []byte("one")}, {name: "safe-skill/references/", mode: os.ModeDir | 0o755}}, code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "multiple roots", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "other/file.txt", content: []byte("x")}}, code: "SKILL_PACKAGE_MULTIPLE_ROOTS"},
		{name: "symlink", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/link", content: []byte("target"), mode: os.ModeSymlink | 0o777}}, code: "SKILL_PACKAGE_SPECIAL_FILE"},
		{name: "encrypted", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: markZIPEncrypted, code: "SKILL_PACKAGE_ENCRYPTED"},
		{name: "ZIP64", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: markZIP64, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "ZIP64 entry", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: markZIPEntry64, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "multi disk", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: markZIPMultiDisk, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "directory bytes", entries: []testArchiveEntry{{name: "safe-skill/", mode: os.ModeDir | 0o755}, {name: "safe-skill/SKILL.md", content: skill}}, mutate: markZIPDirectoryNonEmpty, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "conflicting end record in comment", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: addConflictingZIPComment, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "local header signature", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte {
			value[0] = 'X'
			return value
		}, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "unsupported compression method", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte {
			offset := bytes.Index(value, []byte("PK\x01\x02"))
			binary.LittleEndian.PutUint16(value[offset+10:offset+12], 99)
			return value
		}, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "incorrect member length", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte {
			offset := bytes.Index(value, []byte("PK\x01\x02"))
			size := binary.LittleEndian.Uint32(value[offset+24 : offset+28])
			binary.LittleEndian.PutUint32(value[offset+24:offset+28], size+1)
			return value
		}, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "incorrect member checksum", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte {
			offset := bytes.Index(value, []byte("PK\x01\x02"))
			value[offset+16] ^= 1
			return value
		}, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "short archive", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func([]byte) []byte { return []byte("PK") }, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "directory offset", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: shiftZIPDirectoryOffset, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "directory signature", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: corruptZIPDirectorySignature, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "truncated directory header", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte {
			end := append([]byte{}, value[len(value)-22:]...)
			start := int(binary.LittleEndian.Uint32(end[16:20]))
			// Keep a valid signature but only four declared directory bytes.
			// Even including the end record, a full header cannot be read.
			binary.LittleEndian.PutUint32(end[12:16], 4)
			result := append(append([]byte{}, value[:start+4]...), end...)
			return result[:len(result):len(result)]
		}, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "directory entry bounds", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: expandZIPDirectoryName, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "directory count", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: markZIPCountInconsistent, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "gap before end record", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte {
			end := len(value) - 22
			result := append([]byte{}, value[:end]...)
			result = append(result, 0)
			return append(result, value[end:]...)
		}, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "trailing data", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, mutate: func(value []byte) []byte { return append(value, 'x') }, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "entry count", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/two", content: []byte("2")}}, limits: limitsWith(func(l *skillPackageLimits) { l.files = 1 }), code: "SKILL_PACKAGE_FILE_COUNT_LIMIT"},
		{name: "scanned entry count", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/two", content: []byte("2")}}, mutate: markZIPCountOne, limits: limitsWith(func(l *skillPackageLimits) { l.files = 1 }), code: "SKILL_PACKAGE_FILE_COUNT_LIMIT"},
		{name: "depth", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/a/b.txt", content: []byte("x")}}, limits: limitsWith(func(l *skillPackageLimits) { l.depth = 1 }), code: "SKILL_PACKAGE_DEPTH_LIMIT"},
		{name: "file bytes", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, limits: limitsWith(func(l *skillPackageLimits) { l.fileBytes = int64(len(skill) - 1) }), code: "SKILL_PACKAGE_FILE_SIZE_LIMIT"},
		{name: "expanded bytes", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}, limits: limitsWith(func(l *skillPackageLimits) { l.expandedBytes = int64(len(skill) - 1) }), code: "SKILL_PACKAGE_EXPANDED_SIZE_LIMIT"},
		{name: "expansion ratio", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/zeros", content: make([]byte, 4096)}}, limits: limitsWith(func(l *skillPackageLimits) { l.expansionRatio = 1 }), code: "SKILL_PACKAGE_EXPANSION_RATIO_LIMIT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := makeZIP(t, test.entries)
			if test.mutate != nil {
				inspection, _, findings := inspectSkillPackage(context.Background(), MediaSkillZIP, archive, defaultSkillPackageLimits)
				if inspection.StructureState != catalog.PackageAssessmentPassed || hasErrors(findings) {
					t.Fatalf("unmodified archive inspection/findings = %#v / %#v", inspection, findings)
				}
				archive = test.mutate(archive)
			}
			limits := test.limits
			if limits.archiveBytes == 0 {
				limits = defaultSkillPackageLimits
			}
			inspection, facts, findings := inspectSkillPackage(context.Background(), MediaSkillZIP, archive, limits)
			if inspection.StructureState != catalog.PackageAssessmentFailed || inspection.FileCount != 0 || len(inspection.Files) != 0 || facts != nil || !hasFinding(findings, test.code) {
				t.Fatalf("inspection/facts/findings = %#v / %#v / %#v", inspection, facts, findings)
			}
		})
	}
}

func TestSkillPackageRejectsUnsafeGZIPAndTar(t *testing.T) {
	t.Parallel()
	skill := validPackagedSkill("safe-skill")
	valid := []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}}
	validArchive := makeGZIP(t, valid)
	directoryArchive := makeGZIP(t, []testArchiveEntry{{name: "safe-skill/", typeflag: tar.TypeDir}, valid[0]})
	// Incompressible bytes keep the first member within the ratio budget;
	// tar headers then exhaust it while reading the second regular member.
	data := make([]byte, 2048)
	for offset := 0; offset < len(data); offset += sha256.Size {
		digest := sha256.Sum256([]byte(fmt.Sprintf("archive member block %d", offset)))
		copy(data[offset:], digest[:])
	}
	memberArchive := makeGZIP(t, []testArchiveEntry{valid[0], {name: "safe-skill/data", content: data}})
	for name, archive := range map[string][]byte{"valid trailer": validArchive, "empty directory": directoryArchive, "member under default limits": memberArchive} {
		t.Run(name, func(t *testing.T) {
			inspection, _, findings := inspectSkillPackage(context.Background(), MediaSkillGZIP, archive, defaultSkillPackageLimits)
			if inspection.StructureState != catalog.PackageAssessmentPassed || inspection.FileCount == 0 || hasErrors(findings) {
				t.Fatalf("valid archive inspection/findings = %#v / %#v", inspection, findings)
			}
		})
	}
	corruptTrailer := bytes.Clone(validArchive)
	corruptTrailer[len(corruptTrailer)-8] ^= 1
	tests := []struct {
		name    string
		archive []byte
		limits  skillPackageLimits
		code    string
	}{
		{name: "malformed gzip", archive: []byte("not gzip"), code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "malformed tar", archive: gzipBytes(t, []byte("not tar")), code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "nonempty directory declaration", archive: markTarDirectoryNonEmpty(t, directoryArchive), code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "missing gzip trailer", archive: validArchive[:len(validArchive)-8], code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "incorrect gzip checksum", archive: corruptTrailer, code: "SKILL_PACKAGE_ARCHIVE_INVALID"},
		{name: "expansion ratio during member read", archive: memberArchive, limits: limitsWith(func(l *skillPackageLimits) { l.expansionRatio = 1 }), code: "SKILL_PACKAGE_EXPANSION_RATIO_LIMIT"},
		{name: "traversal", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "../escape", content: []byte("x")}}), code: "SKILL_PACKAGE_PATH_INVALID"},
		{name: "root file", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill", content: skill}}), code: "SKILL_PACKAGE_ROOT_INVALID"},
		{name: "duplicate", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/SKILL.md", content: skill}}), code: "SKILL_PACKAGE_PATH_DUPLICATE"},
		{name: "case collision", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/Notes.txt", content: []byte("one")}, {name: "safe-skill/notes.txt", content: []byte("two")}}), code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "file used as parent", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/references", content: []byte("file")}, {name: "safe-skill/references/README.md", content: []byte("child")}}), code: "SKILL_PACKAGE_PATH_CONFLICT"},
		{name: "multiple roots", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "other-skill/file.txt", content: []byte("x")}}), code: "SKILL_PACKAGE_MULTIPLE_ROOTS"},
		{name: "symlink", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/link", typeflag: tar.TypeSymlink, linkname: "../escape"}}), code: "SKILL_PACKAGE_SPECIAL_FILE"},
		{name: "hard link", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/link", typeflag: tar.TypeLink, linkname: "safe-skill/SKILL.md"}}), code: "SKILL_PACKAGE_SPECIAL_FILE"},
		{name: "fifo", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/fifo", typeflag: tar.TypeFifo}}), code: "SKILL_PACKAGE_SPECIAL_FILE"},
		{name: "entry count", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/two.txt", content: []byte("2")}}), limits: limitsWith(func(l *skillPackageLimits) { l.files = 1 }), code: "SKILL_PACKAGE_FILE_COUNT_LIMIT"},
		{name: "depth", archive: makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/a/b.txt", content: []byte("x")}}), limits: limitsWith(func(l *skillPackageLimits) { l.depth = 1 }), code: "SKILL_PACKAGE_DEPTH_LIMIT"},
		{name: "file bytes", archive: makeGZIP(t, valid), limits: limitsWith(func(l *skillPackageLimits) { l.fileBytes = int64(len(skill) - 1) }), code: "SKILL_PACKAGE_FILE_SIZE_LIMIT"},
		{name: "expanded bytes", archive: makeGZIP(t, valid), limits: limitsWith(func(l *skillPackageLimits) { l.expandedBytes = int64(len(skill) - 1) }), code: "SKILL_PACKAGE_EXPANDED_SIZE_LIMIT"},
		{name: "archive overhead ratio", archive: makeGZIP(t, valid), limits: limitsWith(func(l *skillPackageLimits) { l.expansionRatio = 1 }), code: "SKILL_PACKAGE_EXPANSION_RATIO_LIMIT"},
		{name: "bounded tar padding", archive: appendInsideGZIP(t, makeGZIP(t, valid), make([]byte, 16*1024)), limits: limitsWith(func(l *skillPackageLimits) { l.files = 1; l.expandedBytes = int64(len(skill)) }), code: "SKILL_PACKAGE_EXPANDED_SIZE_LIMIT"},
		{name: "tar trailing data", archive: appendInsideGZIP(t, makeGZIP(t, valid), []byte("not-padding")), code: "SKILL_PACKAGE_TRAILING_DATA"},
		{name: "concatenated gzip", archive: append(makeGZIP(t, valid), makeGZIP(t, valid)...), code: "SKILL_PACKAGE_TRAILING_DATA"},
	}
	ratioArchive := makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}, {name: "safe-skill/zeros", content: make([]byte, 4096)}})
	inspection, _, findings := inspectSkillPackage(context.Background(), MediaSkillGZIP, ratioArchive, limitsWith(func(l *skillPackageLimits) { l.expansionRatio = 1 }))
	if inspection.StructureState != catalog.PackageAssessmentFailed || !hasFinding(findings, "SKILL_PACKAGE_EXPANSION_RATIO_LIMIT") {
		t.Fatalf("GZIP expansion-ratio inspection/findings = %#v / %#v", inspection, findings)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := test.limits
			if limits.archiveBytes == 0 {
				limits = defaultSkillPackageLimits
			}
			inspection, facts, findings := inspectSkillPackage(context.Background(), MediaSkillGZIP, test.archive, limits)
			if inspection.StructureState != catalog.PackageAssessmentFailed || inspection.FileCount != 0 || len(inspection.Files) != 0 || facts != nil || !hasFinding(findings, test.code) {
				t.Fatalf("inspection/facts/findings = %#v / %#v / %#v", inspection, facts, findings)
			}
		})
	}
}

func TestSkillPackageAllowsExactArchiveLimits(t *testing.T) {
	t.Parallel()
	skill := validPackagedSkill("safe-skill")
	entries := []testArchiveEntry{
		{name: "safe-skill/SKILL.md", content: skill},
		{name: "safe-skill/a.txt", content: []byte("a")},
		{name: "safe-skill/b.txt", content: []byte("b")},
	}
	limits := limitsWith(func(l *skillPackageLimits) {
		l.expandedBytes = int64(len(skill) + 2)
		l.fileBytes = int64(len(skill))
		l.files = len(entries)
		l.expansionRatio = 1000
	})
	limits.depth = 1
	for _, tc := range []struct {
		media   string
		content []byte
	}{
		{MediaSkillGZIP, makeGZIP(t, entries)}, {MediaSkillZIP, makeZIP(t, entries)},
	} {
		t.Run(tc.media, func(t *testing.T) {
			limits.archiveBytes = int64(len(tc.content))
			inspection, _, findings := inspectSkillPackage(context.Background(), tc.media, tc.content, limits)
			if inspection.StructureState != catalog.PackageAssessmentPassed || inspection.FileCount != len(entries) || inspection.ExpandedSize != int64(len(skill)+2) || hasErrors(findings) {
				t.Fatalf("exact archive limits inspection/findings = %#v / %#v", inspection, findings)
			}
			// Every individual file still fits; only their aggregate exceeds the
			// budget on the final member. No successful partial inventory may escape.
			over := limits
			over.expandedBytes--
			rejected, facts, failures := inspectSkillPackage(t.Context(), tc.media, tc.content, over)
			if rejected.StructureState != catalog.PackageAssessmentFailed || rejected.FileCount != 0 || len(rejected.Files) != 0 || facts != nil || !hasFinding(failures, "SKILL_PACKAGE_EXPANDED_SIZE_LIMIT") {
				t.Fatalf("aggregate overflow retained partial inspection: %#v / %#v / %#v", rejected, facts, failures)
			}

		})
	}
}

// Gzip comment padding gives the stream an exact 8:1 expansion ratio without
// a large payload, so the complete tar stream reaches the ratio boundary.
func TestSkillPackageGZIPExactExpansionRatio(t *testing.T) {
	skill := validPackagedSkill("safe-skill")
	content := makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: skill}})
	reader, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var padded bytes.Buffer
	writer := gzip.NewWriter(&padded)
	writer.Comment = strings.Repeat("p", len(expanded)/8-len(content)-1)
	if _, err := writer.Write(expanded); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if padded.Len()*8 != len(expanded) {
		t.Fatal("ratio fixture is not exact")
	}
	limits := limitsWith(func(l *skillPackageLimits) { l.expansionRatio = 8 })
	inspection, _, findings := inspectSkillPackage(t.Context(), MediaSkillGZIP, padded.Bytes(), limits)
	if inspection.StructureState != catalog.PackageAssessmentPassed || inspection.FileCount != 1 || hasErrors(findings) {
		t.Fatalf("exact stream ratio: %+v, %+v", inspection, findings)
	}
}

func TestSkillPackageInspectionRejectsMissingOrUnsupportedArchive(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		mediaType string
		content   []byte
		code      string
	}{
		{name: "missing", mediaType: MediaSkillZIP, code: "SKILL_PACKAGE_BYTES_REQUIRED"},
		{name: "unsupported", mediaType: "application/octet-stream", content: []byte("archive"), code: "SKILL_PACKAGE_FORMAT_UNSUPPORTED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, facts, findings := inspectSkillPackage(context.Background(), test.mediaType, test.content, defaultSkillPackageLimits)
			if inspection.StructureState != catalog.PackageAssessmentFailed || facts != nil || !hasFinding(findings, test.code) {
				t.Fatalf("inspection/facts/findings = %#v / %#v / %#v", inspection, facts, findings)
			}
		})
	}
}

func TestSkillPackageFindingsDoNotRetainUnvalidatedEntryNames(t *testing.T) {
	t.Parallel()
	const untrustedName = "safe-skill/credential-looking-entry"
	archive := makeZIP(t, []testArchiveEntry{
		{name: "safe-skill/SKILL.md", content: validPackagedSkill("safe-skill")},
		{name: untrustedName, content: []byte("target"), mode: os.ModeSymlink | 0o777},
	})
	_, _, findings := inspectSkillPackage(context.Background(), MediaSkillZIP, archive, defaultSkillPackageLimits)
	for _, finding := range findings {
		if strings.Contains(finding.Message, untrustedName) {
			t.Fatalf("finding retained unvalidated entry name: %#v", finding)
		}
	}
}

func TestSkillPackageInventoryMediaTypes(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"SKILL.MD":      "text/markdown",
		"notes.txt":     "text/plain",
		"data.json":     "application/json",
		"config.yaml":   "application/yaml",
		"config.yml":    "application/yaml",
		"config.toml":   "application/toml",
		"data.xml":      "application/xml",
		"data.csv":      "text/csv",
		"index.html":    "text/html",
		"index.htm":     "text/html",
		"style.css":     "text/css",
		"client.js":     "text/javascript",
		"module.mjs":    "text/javascript",
		"types.ts":      "text/typescript",
		"tool.py":       "text/x-python",
		"run.sh":        "application/x-sh",
		"icon.png":      "image/png",
		"photo.jpg":     "image/jpeg",
		"photo.jpeg":    "image/jpeg",
		"animation.gif": "image/gif",
		"image.webp":    "image/webp",
		"diagram.svg":   "image/svg+xml",
		"guide.pdf":     "application/pdf",
		"nested.zip":    "application/zip",
		"bundle.gz":     "application/gzip",
		"bundle.gzip":   "application/gzip",
		"LICENSE":       "application/octet-stream",
	}
	for name, expected := range tests {
		if actual := skillFileMediaType(name); actual != expected {
			t.Errorf("skillFileMediaType(%q) = %q, want %q", name, actual, expected)
		}
	}
}

func TestSkillPackageRequiresConformingRootSkill(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []testArchiveEntry
		code    string
	}{
		{name: "empty ZIP", code: "SKILL_PACKAGE_ROOT_INVALID"},
		{name: "missing SKILL", entries: []testArchiveEntry{{name: "safe-skill/readme.md", content: []byte("readme")}}, code: "SKILL_PACKAGE_SKILL_MD_REQUIRED"},
		{name: "invalid root", entries: []testArchiveEntry{{name: "Unsafe/SKILL.md", content: validPackagedSkill("unsafe")}}, code: "SKILL_PACKAGE_ROOT_INVALID"},
		{name: "name mismatch", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: validPackagedSkill("other-skill")}}, code: "SKILL_PACKAGE_NAME_MISMATCH"},
		{name: "frontmatter", entries: []testArchiveEntry{{name: "safe-skill/SKILL.md", content: []byte("# Missing frontmatter")}}, code: "SKILL_FRONTMATTER_REQUIRED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection, _, findings := inspectSkillPackage(context.Background(), MediaSkillZIP, makeZIP(t, test.entries), defaultSkillPackageLimits)
			if inspection.StructureState != catalog.PackageAssessmentFailed || !hasFinding(findings, test.code) {
				t.Fatalf("inspection/findings = %#v / %#v", inspection, findings)
			}
		})
	}
}

func TestSkillPackageFrontmatterBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, fields, code, field string }{
		{name: "valid", fields: "name: example\ndescription: \" Example \"\n"},
		{name: "compatibility", fields: "name: example\ndescription: Example\ncompatibility: \"\"\n", code: "SKILL_COMPATIBILITY_INVALID", field: "compatibility"},
		{name: "name", fields: "name: \" example \"\ndescription: Example\n", code: "SKILL_NAME_INVALID", field: "name"},
		{name: "description", fields: "name: example\ndescription: |\n  " + strings.Repeat("a", 1024) + "\n", code: "SKILL_DESCRIPTION_INVALID", field: "description"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := []testArchiveEntry{{name: "example/SKILL.md", content: []byte("---\n" + test.fields + "---\n# Body\n")}}
			for _, media := range []string{MediaSkillZIP, MediaSkillGZIP} {
				var archive []byte
				if media == MediaSkillGZIP {
					archive = makeGZIP(t, entries)
				} else {
					archive = makeZIP(t, entries)
				}
				result := NewRegistry().Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: media, PackageData: archive})
				if test.code == "" {
					if hasErrors(result.Findings) || result.Package == nil || result.Package.StructureState != catalog.PackageAssessmentPassed || result.NormalizedFacts["description"] != "Example" {
						t.Fatalf("%s valid package: %#v", media, result)
					}
					continue
				}
				if result.Package == nil || result.Package.StructureState != catalog.PackageAssessmentFailed || len(result.Package.Files) != 0 || result.Package.FileCount != 0 || len(result.NormalizedFacts) != 0 {
					t.Fatalf("%s retained successful partial inspection: %#v", media, result)
				}
				if len(result.Findings) != 2 || result.Findings[0].Code != test.code || result.Findings[0].Severity != catalog.SeverityError || result.Findings[0].Path != "/package/SKILL.md/frontmatter/"+test.field || result.Findings[1].Code != "SKILL_PACKAGE_ASSESSMENTS_NOT_ASSESSED" {
					t.Fatalf("%s findings: %#v", media, result.Findings)
				}
			}
		})
	}
}

func TestSkillPackageInspectionHonorsCancellationAndCompressedLimit(t *testing.T) {
	t.Parallel()
	archive := makeZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: validPackagedSkill("safe-skill")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inspection, _, findings := inspectSkillPackage(ctx, MediaSkillZIP, archive, defaultSkillPackageLimits)
	if inspection.StructureState != catalog.PackageAssessmentFailed || !hasFinding(findings, "SKILL_PACKAGE_ARCHIVE_INVALID") {
		t.Fatalf("canceled inspection = %#v / %#v", inspection, findings)
	}
	limits := defaultSkillPackageLimits
	limits.archiveBytes = int64(len(archive) - 1)
	_, _, findings = inspectSkillPackage(context.Background(), MediaSkillZIP, archive, limits)
	if !hasFinding(findings, "SKILL_PACKAGE_COMPRESSED_SIZE_LIMIT") {
		t.Fatalf("compressed limit findings = %#v", findings)
	}
	inspection, _, findings = inspectSkillPackage(ctx, MediaSkillGZIP, makeGZIP(t, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: validPackagedSkill("safe-skill")}}), defaultSkillPackageLimits)
	if inspection.StructureState != catalog.PackageAssessmentFailed || !hasFinding(findings, "SKILL_PACKAGE_ARCHIVE_INVALID") {
		t.Fatalf("canceled GZIP inspection = %#v / %#v", inspection, findings)
	}
}

func TestSkillPackageReadBoundsAndReaderFailures(t *testing.T) {
	t.Parallel()
	limits := defaultSkillPackageLimits
	limits.fileBytes = 1
	inventory := &archiveInventory{limits: limits, archiveSize: 100}
	if _, _, _, err := inventory.readFile(context.Background(), "safe-skill/file", strings.NewReader("x"), 2); err == nil {
		t.Fatal("declared over-limit file was accepted")
	}
	if _, _, _, err := inventory.readFile(context.Background(), "safe-skill/file", strings.NewReader("xx"), 1); err == nil {
		t.Fatal("streamed over-limit file was accepted")
	}

	limits.fileBytes = 100
	inventory = &archiveInventory{limits: limits, archiveSize: 100}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := inventory.readFile(ctx, "safe-skill/file", strings.NewReader("x"), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read returned %v", err)
	}
	sentinel := errors.New("reader failed")
	if _, _, _, err := inventory.readFile(context.Background(), "safe-skill/file", iotest.ErrReader(sentinel), 1); !errors.Is(err, sentinel) {
		t.Fatalf("reader failure returned %v", err)
	}
	if _, _, _, err := inventory.readFile(context.Background(), "safe-skill/file", zeroReader{}, 1); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero-progress read returned %v", err)
	}
	if _, _, _, err := inventory.readFile(context.Background(), "safe-skill/file", strings.NewReader("x"), 2); err == nil {
		t.Fatal("short file was accepted")
	}
}

func TestArchiveReaderRetentionAndAdmission(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"safe-skill/SKILL.md", "safe-skill/asset.txt"} {
		t.Run(name, func(t *testing.T) {
			inventory := &archiveInventory{root: "safe-skill", archiveSize: 3, limits: limitsWith(func(l *skillPackageLimits) { l.fileBytes = 3; l.expandedBytes = 3; l.expansionRatio = 1 })}
			size, digest, retained, err := inventory.readFile(t.Context(), name, strings.NewReader("abc"), 3)
			want := sha256.Sum256([]byte("abc"))
			if err != nil || size != 3 || digest != fmt.Sprintf("sha256:%x", want) {
				t.Fatalf("exact reader budgets: %d, %s, %v", size, digest, err)
			}
			if name == "safe-skill/SKILL.md" {
				if string(retained) != "abc" {
					t.Fatal("Skill document was not retained")
				}
			} else if retained != nil {
				t.Fatal("opaque asset bytes were retained")
			}
		})
	}
	// A short read may stop exactly at the parser limit. Retain the next byte
	// as overflow evidence even when it arrives in a separate read.
	inventory := &archiveInventory{root: "safe-skill", archiveSize: 500_001, limits: defaultSkillPackageLimits}
	reader := io.MultiReader(strings.NewReader(strings.Repeat("x", 500_000)), strings.NewReader("x"))
	size, _, retained, err := inventory.readFile(t.Context(), "safe-skill/SKILL.md", reader, 500_001)
	if err != nil || size != 500_001 || len(retained) != 500_001 {
		t.Fatalf("overflow evidence lost across a short read: size=%d retained=%d err=%v", size, len(retained), err)
	}
	for _, tc := range []struct {
		name           string
		size, retained int64
		documents      int
		accepted       bool
	}{
		{"exact document bytes", catalog.MaxPluginDocumentBytes, 0, 0, true},
		{"oversized document", catalog.MaxPluginDocumentBytes + 1, 0, 0, false},
		{"last document slot", 1, 0, catalog.MaxPluginComponents + 1, true},
		{"document slots exhausted", 1, 0, catalog.MaxPluginComponents + 2, false},
		{"exact aggregate bytes", 1, catalog.MaxCatalogDocumentBytes - 1, 0, true},
		{"aggregate exhausted", 1, catalog.MaxCatalogDocumentBytes, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := ""
			inventory := &archiveInventory{plugin: true, pluginRoot: &root, documents: map[string][]byte{}, retainedBytes: tc.retained, archiveSize: catalog.MaxPluginDocumentBytes, limits: defaultSkillPackageLimits}
			for index := 0; index < tc.documents; index++ {
				inventory.documents[fmt.Sprintf("skills/skill-%d/SKILL.md", index)] = nil
			}
			reads := 0
			data := strings.NewReader(strings.Repeat("x", int(tc.size)))
			reader := readerFunc(func(buffer []byte) (int, error) { reads++; return data.Read(buffer) })
			size, _, retained, err := inventory.readFile(t.Context(), "plugin.json", reader, tc.size)
			if tc.accepted {
				if err != nil || size != tc.size || int64(len(retained)) != tc.size || reads == 0 {
					t.Fatalf("admitted document: size=%d retained=%d reads=%d err=%v", size, len(retained), reads, err)
				}
			} else {
				var violation *skillPackageError
				if !errors.As(err, &violation) || violation.code != "SKILL_PACKAGE_FILE_SIZE_LIMIT" || reads != 0 {
					t.Fatalf("document admission read=%d err=%v", reads, err)
				}
			}
		})
	}
}

func TestBoundedCountingReaderStopsAtLimit(t *testing.T) {
	t.Parallel()
	reader := &boundedCountingReader{reader: strings.NewReader("abcdef"), remaining: 3}
	buffer := make([]byte, 8)
	read, err := reader.Read(buffer)
	if err != nil || read != 3 || string(buffer[:read]) != "abc" || reader.count != 3 || reader.remaining != 0 {
		t.Fatalf("bounded read = %d, %v, %q; state = %#v", read, err, buffer[:read], reader)
	}
	if _, err := reader.Read(buffer); !errors.Is(err, errSkillArchiveExpansionLimit) {
		t.Fatalf("read beyond limit returned %v", err)
	}
	exact := &boundedCountingReader{reader: strings.NewReader("abc"), remaining: 3}
	if read, err := exact.Read(buffer); err != nil || read != 3 {
		t.Fatalf("exact bounded read = %d, %v", read, err)
	}
	if read, err := exact.Read(buffer); read != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("exact bounded EOF = %d, %v", read, err)
	}
	ratio := &boundedCountingReader{reader: strings.NewReader("ab"), remaining: 1, limitError: errSkillArchiveRatioLimit}
	if _, err := ratio.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := ratio.Read(buffer); !errors.Is(err, errSkillArchiveRatioLimit) {
		t.Fatalf("ratio limit returned %v", err)
	}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := &boundedCountingReader{reader: readerFunc(func(buffer []byte) (int, error) {
		if err := canceledContext.Err(); err != nil {
			return 0, err
		}
		return strings.NewReader("a").Read(buffer)
	}), remaining: 1}
	if _, err := canceled.Read(buffer); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounded read returned %v", err)
	}
	// After consuming the budget, the one-byte probe must reject a stalled
	// underlying reader rather than treating it as a clean end of archive.
	stalled := &boundedCountingReader{reader: zeroReader{}}
	if n, err := stalled.Read(buffer); n != 0 || !errors.Is(err, io.ErrNoProgress) || stalled.count != 0 || stalled.remaining != 0 {
		t.Fatalf("stalled limit probe = %d, %v; state = %#v", n, err, stalled)
	}
	violation := &skillPackageError{message: "bounded failure"}
	if violation.Error() != "bounded failure" {
		t.Fatalf("error message = %q", violation.Error())
	}
}

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

func FuzzSkillPackageValidation(f *testing.F) {
	f.Add(byte(0), makeZIP(f, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: validPackagedSkill("safe-skill")}}))
	f.Add(byte(1), makeGZIP(f, []testArchiveEntry{{name: "safe-skill/SKILL.md", content: validPackagedSkill("safe-skill")}}))
	handoff := &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/skills/safe-skill@sha256:" + strings.Repeat("a", 64)}}
	registry := NewRegistry()
	f.Fuzz(func(t *testing.T, format byte, content []byte) {
		if len(content) > 1<<20 {
			t.Skip()
		}
		mediaType := MediaSkillZIP
		if format&1 == 1 {
			mediaType = MediaSkillGZIP
		}
		_ = registry.Validate(context.Background(), catalog.KindSkill, catalog.RepresentationInput{MediaType: mediaType, PackageData: content, Handoff: handoff})
	})
}

func makeZIP(tb testing.TB, entries []testArchiveEntry) []byte {
	tb.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.NonUTF8 = entry.nonUTF8
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := file.Write(entry.content); err != nil {
			tb.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	return buffer.Bytes()
}

func makeGZIP(tb testing.TB, entries []testArchiveEntry) []byte {
	tb.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Mode: int64(mode.Perm()), Size: int64(len(entry.content)), Typeflag: typeflag, Linkname: entry.linkname, Format: tar.FormatPAX}
		if typeflag != tar.TypeReg && typeflag != 0 {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			tb.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write(entry.content); err != nil {
				tb.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		tb.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		tb.Fatal(err)
	}
	return buffer.Bytes()
}

func appendInsideGZIP(tb testing.TB, archive, trailing []byte) []byte {
	tb.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		tb.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		tb.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		tb.Fatal(err)
	}
	return gzipBytes(tb, append(content, trailing...))
}

func gzipBytes(tb testing.TB, content []byte) []byte {
	tb.Helper()
	var result bytes.Buffer
	writer := gzip.NewWriter(&result)
	if _, err := writer.Write(content); err != nil {
		tb.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	return result.Bytes()
}

func markZIPEncrypted(content []byte) []byte {
	result := append([]byte(nil), content...)
	for offset := 0; offset+10 <= len(result); offset++ {
		switch string(result[offset : offset+4]) {
		case "PK\x03\x04":
			result[offset+6] |= 1
		case "PK\x01\x02":
			result[offset+8] |= 1
		}
	}
	return result
}

// addConflictingZIPComment preserves the outer directory bounds while adding
// an inconsistent end record inside the comment for the ZIP reader to reject.
func addConflictingZIPComment(content []byte) []byte {
	result := bytes.Clone(content)
	// The real end record describes the complete comment. Its nested signature
	// advertises one directory entry without a matching directory.
	const commentLength = 23
	comment := make([]byte, commentLength)
	copy(comment, "PK\x05\x06")
	binary.LittleEndian.PutUint16(comment[8:10], 1)
	binary.LittleEndian.PutUint16(comment[10:12], 1)
	binary.LittleEndian.PutUint16(result[len(result)-2:], commentLength)
	return append(result, comment...)
}

// markTarDirectoryNonEmpty changes the first directory's declared size while
// retaining a valid tar header checksum and gzip envelope.
func markTarDirectoryNonEmpty(tb testing.TB, archive []byte) []byte {
	tb.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		tb.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		tb.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		tb.Fatal(err)
	}
	if len(content) < 512 || content[156] != tar.TypeDir {
		tb.Fatal("fixture must begin with a tar directory header")
	}
	// Keep a valid tar header checksum so the directory-size guard, rather
	// than a malformed-header error, rejects this declaration.
	copy(content[124:136], "00000000001\x00")
	copy(content[148:156], "        ")
	var checksum int
	for _, value := range content[:512] {
		checksum += int(value)
	}
	copy(content[148:156], fmt.Sprintf("%06o\x00 ", checksum))
	return gzipBytes(tb, content)
}

func markZIP64(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.LastIndex(result, []byte("PK\x05\x06"))
	if offset >= 0 {
		result[offset+8] = 0xff
		result[offset+9] = 0xff
		result[offset+10] = 0xff
		result[offset+11] = 0xff
	}
	return result
}

func markZIPEntry64(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.Index(result, []byte("PK\x01\x02"))
	if offset >= 0 {
		copy(result[offset+24:offset+28], []byte{0xff, 0xff, 0xff, 0xff})
	}
	return result
}

func markZIPDirectoryNonEmpty(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.Index(result, []byte("PK\x01\x02"))
	if offset >= 0 {
		result[offset+24] = 1
	}
	return result
}

func markZIPMultiDisk(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.LastIndex(result, []byte("PK\x05\x06"))
	if offset >= 0 {
		result[offset+4] = 1
	}
	return result
}

func shiftZIPDirectoryOffset(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.LastIndex(result, []byte("PK\x05\x06"))
	if offset >= 0 {
		result[offset+16]++
	}
	return result
}

func corruptZIPDirectorySignature(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.Index(result, []byte("PK\x01\x02"))
	if offset >= 0 {
		result[offset] = 'X'
	}
	return result
}

func expandZIPDirectoryName(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.Index(result, []byte("PK\x01\x02"))
	if offset >= 0 {
		result[offset+28] = 0xff
		result[offset+29] = 0xff
	}
	return result
}

func markZIPCountInconsistent(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.LastIndex(result, []byte("PK\x05\x06"))
	if offset >= 0 {
		result[offset+8] = 2
		result[offset+10] = 2
	}
	return result
}

func markZIPCountOne(content []byte) []byte {
	result := append([]byte(nil), content...)
	offset := bytes.LastIndex(result, []byte("PK\x05\x06"))
	if offset >= 0 {
		result[offset+8] = 1
		result[offset+10] = 1
	}
	return result
}

func validPackagedSkill(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: A safely inspected Agent Skill package.\n---\n# Instructions\n")
}

func limitsWith(change func(*skillPackageLimits)) skillPackageLimits {
	limits := defaultSkillPackageLimits
	change(&limits)
	return limits
}
