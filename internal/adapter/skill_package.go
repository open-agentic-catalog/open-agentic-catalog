// Archive inspection enforces shared Skill and Plugin resource limits before
// producing deterministic file inventories and retained validation findings.

package adapter

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const (
	skillDocumentName             = "SKILL.md"
	skillPackageDataPath          = "/packageData"
	skillArchiveEntryLimitMessage = "The Skill package contains too many archive entries."
)

type skillPackageLimits struct {
	archiveBytes   int64
	expandedBytes  int64
	fileBytes      int64
	files          int
	depth          int
	expansionRatio int64
}

var defaultSkillPackageLimits = skillPackageLimits{
	archiveBytes:   catalog.MaxSkillPackageBytes,
	expandedBytes:  catalog.MaxSkillPackageExpandedBytes,
	fileBytes:      catalog.MaxSkillPackageFileBytes,
	files:          catalog.MaxSkillPackageFiles,
	depth:          catalog.MaxSkillPackageDepth,
	expansionRatio: catalog.MaxSkillPackageExpansionRatio,
}

type skillPackageError struct {
	code    string
	path    string
	message string
}

// Error returns the bounded archive-validation message.
func (v *skillPackageError) Error() string { return v.message }

var errSkillArchiveExpansionLimit = errors.New("skill package archive expansion limit exceeded")

var errSkillArchiveRatioLimit = errors.New("skill package archive expansion ratio exceeded")

type archiveInventory struct {
	// Plugin archives use the same transport/path guards with root discovery
	// deferred until the complete bounded inventory has been read.
	plugin        bool
	pluginRoot    *string
	documents     map[string][]byte
	retainedBytes int64
	limits        skillPackageLimits
	archiveSize   int64
	entries       int
	root          string
	seen          map[string]bool
	foldedFiles   map[string]string
	foldedDirs    map[string]string
	files         []catalog.SkillPackageFile
	expandedSize  int64
	skillContent  []byte
}

// inspectSkillPackage checks archive structure and Skill frontmatter under the
// supplied work limits and returns retainable inventory and findings.
func inspectSkillPackage(ctx context.Context, mediaType string, content []byte, limits skillPackageLimits) (*catalog.SkillPackageInspection, map[string]any, []catalog.Finding) {
	failed := &catalog.SkillPackageInspection{StructureState: catalog.PackageAssessmentFailed, Assessments: catalog.NotAssessedSkillPackageAssessments()}
	if len(content) == 0 {
		return failed, nil, packageFindings(&skillPackageError{code: "SKILL_PACKAGE_BYTES_REQUIRED", path: skillPackageDataPath, message: "An inspected Skill package requires non-empty base64 packageData."})
	}
	if int64(len(content)) > limits.archiveBytes {
		return failed, nil, packageFindings(&skillPackageError{code: "SKILL_PACKAGE_COMPRESSED_SIZE_LIMIT", path: skillPackageDataPath, message: "The compressed Skill package exceeds the supported byte limit."})
	}
	inventory := &archiveInventory{
		limits:      limits,
		archiveSize: int64(len(content)),
		seen:        make(map[string]bool),
		foldedFiles: make(map[string]string),
		foldedDirs:  make(map[string]string),
	}
	var err error
	switch mediaType {
	case MediaSkillZIP:
		err = inventory.inspectZIP(ctx, content)
	case MediaSkillGZIP:
		err = inventory.inspectGZIP(ctx, content)
	default:
		err = &skillPackageError{code: "SKILL_PACKAGE_FORMAT_UNSUPPORTED", path: "/mediaType", message: "The Skill package format is not supported for archive inspection."}
	}
	if err != nil {
		var violation *skillPackageError
		if errors.As(err, &violation) {
			return failed, nil, packageFindings(violation)
		}
		return failed, nil, packageFindings(&skillPackageError{code: "SKILL_PACKAGE_ARCHIVE_INVALID", path: skillPackageDataPath, message: "The Skill package is malformed or cannot be read completely."})
	}
	if inventory.root == "" || !catalog.ValidSkillName(inventory.root) {
		return failed, nil, packageFindings(&skillPackageError{code: "SKILL_PACKAGE_ROOT_INVALID", path: skillPackageDataPath, message: "The package must contain one lower-case Agent Skill root directory."})
	}
	if inventory.skillContent == nil {
		return failed, nil, packageFindings(&skillPackageError{code: "SKILL_PACKAGE_SKILL_MD_REQUIRED", path: skillPackageDataPath, message: "The Agent Skill root directory must contain exactly one regular SKILL.md file."})
	}
	frontmatter, markdownFindings := parseSkillFrontmatter(string(inventory.skillContent))
	if hasErrorFinding(markdownFindings) {
		for index := range markdownFindings {
			markdownFindings[index].Path = "/package/SKILL.md" + markdownFindings[index].Path
		}
		markdownFindings = append(markdownFindings, assessmentNotAssessedFinding())
		return failed, nil, markdownFindings
	}
	if frontmatter["name"] != inventory.root {
		return failed, nil, packageFindings(&skillPackageError{code: "SKILL_PACKAGE_NAME_MISMATCH", path: "/package/SKILL.md/frontmatter/name", message: "SKILL.md name must match its package root directory."})
	}
	sort.Slice(inventory.files, func(i, j int) bool { return inventory.files[i].Path < inventory.files[j].Path })
	inspection := &catalog.SkillPackageInspection{
		StructureState: catalog.PackageAssessmentPassed,
		RootDirectory:  inventory.root,
		ExpandedSize:   inventory.expandedSize,
		FileCount:      len(inventory.files),
		Files:          inventory.files,
		Assessments:    catalog.NotAssessedSkillPackageAssessments(),
	}
	facts := map[string]any{
		"name":             frontmatter["name"],
		"description":      frontmatter["description"],
		"packageFormat":    mediaType,
		"structureState":   string(catalog.PackageAssessmentPassed),
		"fileCount":        inspection.FileCount,
		"expandedSize":     inspection.ExpandedSize,
		"assessmentStates": inspection.Assessments,
	}
	return inspection, facts, []catalog.Finding{assessmentNotAssessedFinding()}
}

// inspectZIP validates ZIP directory bounds and streams regular entries through
// shared path and expansion checks.
func (i *archiveInventory) inspectZIP(ctx context.Context, content []byte) error {
	if err := preflightZIPDirectory(content, i.limits.files); err != nil {
		return err
	}
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.NonUTF8 {
			return &skillPackageError{code: "SKILL_PACKAGE_PATH_INVALID", path: skillPackageDataPath, message: "ZIP entry names must use unambiguous UTF-8 paths."}
		}
		if file.Flags&1 != 0 {
			return &skillPackageError{code: "SKILL_PACKAGE_ENCRYPTED", path: skillPackageDataPath, message: "Encrypted Skill package entries cannot be inspected and are rejected."}
		}
		mode := file.Mode()
		isDirectory := file.FileInfo().IsDir()
		if !isDirectory && !mode.IsRegular() {
			return specialFileViolation()
		}
		if isDirectory && file.UncompressedSize64 != 0 {
			return &skillPackageError{code: "SKILL_PACKAGE_ARCHIVE_INVALID", path: skillPackageDataPath, message: "A ZIP directory entry contains unexpected file bytes."}
		}
		if i.limits.fileBytes < 0 {
			return fileSizeViolation()
		}
		maxFileBytes := uint64(i.limits.fileBytes) // #nosec G115 -- a nonnegative int64 always fits uint64.
		if file.UncompressedSize64 > maxFileBytes {
			return fileSizeViolation()
		}
		declaredSize := int64(file.UncompressedSize64) // #nosec G115 -- bounded above by the nonnegative int64 file limit.
		entryPath, err := i.beginEntry(file.Name, isDirectory)
		if err != nil || isDirectory {
			if err != nil {
				return err
			}
			continue
		}
		entryReader, err := file.Open()
		if err != nil {
			return err
		}
		readSize, digest, skillContent, readErr := i.readFile(ctx, entryPath, entryReader, declaredSize)
		closeErr := entryReader.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil || readSize != declaredSize {
			return &skillPackageError{code: "SKILL_PACKAGE_ARCHIVE_INVALID", path: skillPackageDataPath, message: "A ZIP entry did not match its declared complete bytes."}
		}
		i.addFile(entryPath, readSize, digest, mode.Perm()&0o111 != 0, skillContent)
	}
	return nil
}

// preflightZIPDirectory bounds the central directory before archive/zip
// allocates one File value per declared entry. Packages within the supported
// limits do not need multi-disk or ZIP64 records, so those ambiguous variants
// are rejected rather than parsed through a larger compatibility surface.
func preflightZIPDirectory(content []byte, maximumEntries int) error {
	const (
		endHeaderBytes     = 22
		maxCommentBytes    = 1<<16 - 1
		directoryHeaderLen = 46
	)
	if len(content) < endHeaderBytes {
		return io.ErrUnexpectedEOF
	}
	searchStart := max(0, len(content)-endHeaderBytes-maxCommentBytes)
	endOffset := -1
	for offset := len(content) - endHeaderBytes; offset >= searchStart; offset-- {
		if binary.LittleEndian.Uint32(content[offset:offset+4]) != 0x06054b50 {
			continue
		}
		commentBytes := int(binary.LittleEndian.Uint16(content[offset+20 : offset+22]))
		if offset+endHeaderBytes+commentBytes == len(content) {
			endOffset = offset
			break
		}
	}
	if endOffset < 0 {
		return errors.New("zip end-of-directory record is missing")
	}
	end := content[endOffset : endOffset+endHeaderBytes]
	recordsOnDisk := binary.LittleEndian.Uint16(end[8:10])
	totalRecords := binary.LittleEndian.Uint16(end[10:12])
	directoryBytes := binary.LittleEndian.Uint32(end[12:16])
	directoryOffset := binary.LittleEndian.Uint32(end[16:20])
	if binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 || recordsOnDisk != totalRecords || totalRecords == 0xffff || directoryBytes == 0xffffffff || directoryOffset == 0xffffffff {
		return errors.New("multi-disk and ZIP64 Skill packages are not supported")
	}
	if int(totalRecords) > maximumEntries {
		return &skillPackageError{code: "SKILL_PACKAGE_FILE_COUNT_LIMIT", path: skillPackageDataPath, message: skillArchiveEntryLimitMessage}
	}
	directoryStart := uint64(directoryOffset)
	directoryEnd := directoryStart + uint64(directoryBytes)
	if directoryEnd != uint64(endOffset) || directoryEnd > uint64(len(content)) {
		return errors.New("zip central directory bounds are invalid")
	}
	position := directoryStart
	count := 0
	for position < directoryEnd {
		if directoryEnd-position < directoryHeaderLen || binary.LittleEndian.Uint32(content[position:position+4]) != 0x02014b50 {
			return errors.New("zip central directory entry is invalid")
		}
		header := content[position : position+directoryHeaderLen]
		if binary.LittleEndian.Uint32(header[20:24]) == 0xffffffff || binary.LittleEndian.Uint32(header[24:28]) == 0xffffffff || binary.LittleEndian.Uint16(header[34:36]) == 0xffff || binary.LittleEndian.Uint32(header[42:46]) == 0xffffffff {
			return errors.New("zip64 Skill package entries are not supported")
		}
		variableBytes := uint64(binary.LittleEndian.Uint16(header[28:30])) + uint64(binary.LittleEndian.Uint16(header[30:32])) + uint64(binary.LittleEndian.Uint16(header[32:34]))
		position += directoryHeaderLen + variableBytes
		if position > directoryEnd {
			return errors.New("zip central directory entry exceeds its declared bounds")
		}
		count++
		if count > maximumEntries {
			return &skillPackageError{code: "SKILL_PACKAGE_FILE_COUNT_LIMIT", path: skillPackageDataPath, message: skillArchiveEntryLimitMessage}
		}
	}
	if count != int(totalRecords) {
		return errors.New("zip central directory entry count is inconsistent")
	}
	return nil
}

// inspectGZIP walks a bounded gzip-compressed tar stream while rejecting
// special files and expansion-limit violations.
func (i *archiveInventory) inspectGZIP(ctx context.Context, content []byte) error {
	compressed := bytes.NewReader(content)
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	reader.Multistream(false)
	// The regular-file budget does not include tar headers, padding, or bounded
	// PAX/GNU path metadata. Allow a conservative 4 KiB per visible entry plus
	// the end markers, while applying the expansion ratio to the entire tar
	// stream as it is decompressed rather than only after the stream completes.
	expandedStreamLimit := i.limits.expandedBytes + int64(i.limits.files)*4096 + 2048
	ratioStreamLimit := i.archiveSize * i.limits.expansionRatio
	streamLimit := min(expandedStreamLimit, ratioStreamLimit)
	limitError := errSkillArchiveExpansionLimit
	if ratioStreamLimit < expandedStreamLimit {
		limitError = errSkillArchiveRatioLimit
	}
	cancellableReader := readerFunc(func(buffer []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return reader.Read(buffer)
	})
	counted := &boundedCountingReader{reader: cancellableReader, remaining: streamLimit, limitError: limitError}
	tarReader := tar.NewReader(counted)
	for {
		if err := ctx.Err(); err != nil {
			_ = reader.Close()
			return err
		}
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			_ = reader.Close()
			if violation := archiveStreamViolation(nextErr); violation != nil {
				return violation
			}
			return nextErr
		}
		isDirectory := header.Typeflag == tar.TypeDir
		if !isDirectory && header.Typeflag != tar.TypeReg && header.Typeflag != 0 {
			_ = reader.Close()
			return specialFileViolation()
		}
		if isDirectory && header.Size != 0 {
			_ = reader.Close()
			return &skillPackageError{code: "SKILL_PACKAGE_ARCHIVE_INVALID", path: skillPackageDataPath, message: "A tar directory entry contains unexpected file bytes."}
		}
		if header.Size < 0 || header.Size > i.limits.fileBytes {
			_ = reader.Close()
			return fileSizeViolation()
		}
		entryPath, entryErr := i.beginEntry(header.Name, isDirectory)
		if entryErr != nil {
			_ = reader.Close()
			return entryErr
		}
		if isDirectory {
			continue
		}
		readSize, digest, skillContent, readErr := i.readFile(ctx, entryPath, tarReader, header.Size)
		if readErr != nil {
			_ = reader.Close()
			if violation := archiveStreamViolation(readErr); violation != nil {
				return violation
			}
			return readErr
		}
		if readSize != header.Size {
			_ = reader.Close()
			return &skillPackageError{code: "SKILL_PACKAGE_ARCHIVE_INVALID", path: skillPackageDataPath, message: "A tar entry did not match its declared complete bytes."}
		}
		i.addFile(entryPath, readSize, digest, header.Mode&0o111 != 0, skillContent)
	}
	var trailing [32 * 1024]byte
	for {
		read, readErr := counted.Read(trailing[:])
		for _, value := range trailing[:read] {
			if value != 0 {
				_ = reader.Close()
				return &skillPackageError{code: "SKILL_PACKAGE_TRAILING_DATA", path: skillPackageDataPath, message: "The gzipped tarball contains data after the archive end marker."}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = reader.Close()
			if violation := archiveStreamViolation(readErr); violation != nil {
				return violation
			}
			return readErr
		}
	}
	if err := reader.Close(); err != nil {
		return err
	}
	if compressed.Len() != 0 {
		return &skillPackageError{code: "SKILL_PACKAGE_TRAILING_DATA", path: skillPackageDataPath, message: "The Skill package must contain exactly one gzip member."}
	}
	if counted.count > i.archiveSize*i.limits.expansionRatio {
		return ratioViolation()
	}
	return nil
}

// beginEntry validates and accounts for an archive path before accepting its
// directory or file content.
func (i *archiveInventory) beginEntry(name string, directory bool) (string, error) {
	i.entries++
	if i.entries > i.limits.files {
		return "", &skillPackageError{code: "SKILL_PACKAGE_FILE_COUNT_LIMIT", path: skillPackageDataPath, message: skillArchiveEntryLimitMessage}
	}
	name = strings.TrimSuffix(name, "/")
	normalized, ok := catalog.NormalizeSkillPackagePath(name)
	if !ok {
		return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_INVALID", path: skillPackageDataPath, message: "A Skill package entry has an absolute, traversing, ambiguous, or over-deep path."}
	}
	segments := strings.Split(normalized, "/")
	if len(segments)-1 > i.limits.depth {
		return "", &skillPackageError{code: "SKILL_PACKAGE_DEPTH_LIMIT", path: skillPackageDataPath, message: "A Skill package entry exceeds the supported directory depth."}
	}
	if !i.plugin && i.root == "" {
		i.root = segments[0]
	}
	if !i.plugin && segments[0] != i.root {
		return "", &skillPackageError{code: "SKILL_PACKAGE_MULTIPLE_ROOTS", path: skillPackageDataPath, message: "The package must contain exactly one Agent Skill root directory."}
	}
	if !i.plugin && !directory && len(segments) < 2 {
		return "", &skillPackageError{code: "SKILL_PACKAGE_ROOT_INVALID", path: skillPackageDataPath, message: "Regular files must be contained inside the Agent Skill root directory."}
	}
	if _, duplicate := i.seen[normalized]; duplicate {
		return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_DUPLICATE", path: skillPackageDataPath, message: "The Skill package contains duplicate normalized entry paths."}
	}
	folded := strings.ToLower(normalized)
	if existing, exists := i.foldedFiles[folded]; exists && existing != normalized {
		return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_CONFLICT", path: skillPackageDataPath, message: "The Skill package contains regular-file paths that differ only by letter case."}
	}
	if existing, exists := i.foldedDirs[folded]; exists {
		if !directory {
			return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_CONFLICT", path: skillPackageDataPath, message: "A regular Skill package file cannot also be an entry path parent."}
		}
		if existing != normalized {
			return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_CONFLICT", path: skillPackageDataPath, message: "The Skill package contains directory paths that differ only by letter case."}
		}
	}
	for ancestor := path.Dir(normalized); ancestor != "."; ancestor = path.Dir(ancestor) {
		foldedAncestor := strings.ToLower(ancestor)
		if _, exists := i.foldedFiles[foldedAncestor]; exists {
			return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_CONFLICT", path: skillPackageDataPath, message: "A regular Skill package file cannot also be an entry path parent."}
		}
		if existing, exists := i.foldedDirs[foldedAncestor]; exists && existing != ancestor {
			return "", &skillPackageError{code: "SKILL_PACKAGE_PATH_CONFLICT", path: skillPackageDataPath, message: "The Skill package contains directory paths that differ only by letter case."}
		}
		i.foldedDirs[foldedAncestor] = ancestor
	}
	i.seen[normalized] = directory
	if directory {
		i.foldedDirs[folded] = normalized
	} else {
		i.foldedFiles[folded] = normalized
	}
	return normalized, nil
}

// readFile streams one entry under cancellation and size budgets, computing its
// digest and retaining only needed Skill content.
func (i *archiveInventory) readFile(ctx context.Context, entryPath string, reader io.Reader, declaredSize int64) (int64, string, []byte, error) {
	if declaredSize > i.limits.fileBytes {
		return 0, "", nil, fileSizeViolation()
	}
	digest := sha256.New()
	captureSkill := entryPath == i.root+"/"+skillDocumentName
	if i.plugin {
		captureSkill = i.pluginDocument(entryPath)
		if captureSkill && (declaredSize > catalog.MaxPluginDocumentBytes || len(i.documents) >= catalog.MaxPluginComponents+2 || declaredSize > catalog.MaxCatalogDocumentBytes-i.retainedBytes) {
			return 0, "", nil, fileSizeViolation()
		}
	}
	var skillContent []byte
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", nil, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			size += int64(read)
			if size > i.limits.fileBytes {
				return 0, "", nil, fileSizeViolation()
			}
			if size > i.limits.expandedBytes-i.expandedSize {
				return 0, "", nil, expansionViolation()
			}
			if i.expandedSize+size > i.archiveSize*i.limits.expansionRatio {
				return 0, "", nil, ratioViolation()
			}
			_, _ = digest.Write(buffer[:read])
			if captureSkill && len(skillContent) <= 500_000 {
				remaining := 500_001 - len(skillContent)
				skillContent = append(skillContent, buffer[:min(read, remaining)]...)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, "", nil, err
		}
		if read == 0 {
			return 0, "", nil, io.ErrNoProgress
		}
	}
	if size != declaredSize {
		return 0, "", nil, &skillPackageError{code: "SKILL_PACKAGE_ARCHIVE_INVALID", path: skillPackageDataPath, message: "A package entry did not match its declared size."}
	}
	return size, digestString(digest), skillContent, nil
}

// addFile records a validated regular file's inventory metadata and captures
// Skill frontmatter content when applicable.
func (i *archiveInventory) addFile(entryPath string, size int64, digest string, executable bool, skillContent []byte) {
	i.expandedSize += size
	i.files = append(i.files, catalog.SkillPackageFile{Path: entryPath, Size: size, MediaType: skillFileMediaType(entryPath), Digest: digest, Executable: executable})
	if i.plugin && i.pluginDocument(entryPath) {
		i.documents[entryPath] = skillContent
		i.retainedBytes += int64(len(skillContent))
	}
	if entryPath == i.root+"/"+skillDocumentName {
		i.skillContent = skillContent
	}
}

// The first Plugin pass validates inventory and determines the root. Only the
// second pass retains exact documents at that root's portable component paths;
// similarly named files in extensions remain opaque and are never decoded.
func (i *archiveInventory) pluginDocument(name string) bool {
	if i.pluginRoot == nil {
		return false
	}
	if *i.pluginRoot != "" {
		name = strings.TrimPrefix(name, *i.pluginRoot+"/")
	}
	return name == pluginManifestName || name == pluginMCPName || strings.HasPrefix(name, "skills/") && len(strings.Split(name, "/")) == 3 && path.Base(name) == skillDocumentName
}

type boundedCountingReader struct {
	reader     io.Reader
	remaining  int64
	count      int64
	limitError error
}

// Read counts expanded bytes and returns a package-limit error when the
// configured budget is exceeded.
func (r *boundedCountingReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		read, err := r.reader.Read(probe[:])
		r.count += int64(read)
		if read > 0 {
			if r.limitError != nil {
				return 0, r.limitError
			}
			return 0, errSkillArchiveExpansionLimit
		}
		if err != nil {
			return 0, err
		}
		return 0, io.ErrNoProgress
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	read, err := r.reader.Read(buffer)
	r.remaining -= int64(read)
	r.count += int64(read)
	return read, err
}

type readerFunc func([]byte) (int, error)

// Read adapts the supplied function to the reader interface used by bounded
// archive streams.
func (f readerFunc) Read(buffer []byte) (int, error) {
	return f(buffer)
}

// archiveStreamViolation maps known expansion-reader errors to stable archive
// findings and returns nil for unrelated errors.
func archiveStreamViolation(err error) *skillPackageError {
	switch {
	case errors.Is(err, errSkillArchiveRatioLimit):
		return ratioViolation()
	case errors.Is(err, errSkillArchiveExpansionLimit):
		return expansionViolation()
	default:
		return nil
	}
}

// digestString formats the accumulated hash with the retained sha256: prefix.
func digestString(value hash.Hash) string {
	return "sha256:" + hex.EncodeToString(value.Sum(nil))
}

// skillFileMediaType maps a reviewed extension vocabulary to inventory media
// types, falling back to opaque bytes.
func skillFileMediaType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".toml":
		return "application/toml"
	case ".xml":
		return "application/xml"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".mjs":
		return "text/javascript"
	case ".ts":
		return "text/typescript"
	case ".py":
		return "text/x-python"
	case ".sh":
		return "application/x-sh"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".gz", ".gzip":
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}

// packageFindings projects an archive violation into bounded validation
// evidence.
func packageFindings(violation *skillPackageError) []catalog.Finding {
	return []catalog.Finding{
		{Code: violation.code, Severity: catalog.SeverityError, Path: violation.path, Message: violation.message},
		assessmentNotAssessedFinding(),
	}
}

// assessmentNotAssessedFinding records that archive structure validation does
// not perform deferred package security assessments.
func assessmentNotAssessedFinding() catalog.Finding {
	return catalog.Finding{Code: "SKILL_PACKAGE_ASSESSMENTS_NOT_ASSESSED", Severity: catalog.SeverityInfo, Path: "/package/assessments", Message: "Optional malware, secret, license, SBOM, and provenance assessments have not been performed."}
}

// specialFileViolation describes an archive member type that cannot be safely
// retained as a regular package file.
func specialFileViolation() *skillPackageError {
	return &skillPackageError{code: "SKILL_PACKAGE_SPECIAL_FILE", path: skillPackageDataPath, message: "A Skill package entry is a link or special file; only regular files and directories are allowed."}
}

// fileSizeViolation describes a regular file exceeding the supported per-file
// budget.
func fileSizeViolation() *skillPackageError {
	return &skillPackageError{code: "SKILL_PACKAGE_FILE_SIZE_LIMIT", path: skillPackageDataPath, message: "A Skill package file exceeds the supported per-file byte limit."}
}

// expansionViolation describes an archive exceeding its aggregate expanded-byte
// budget.
func expansionViolation() *skillPackageError {
	return &skillPackageError{code: "SKILL_PACKAGE_EXPANDED_SIZE_LIMIT", path: skillPackageDataPath, message: "The Skill package exceeds the supported expanded-byte limit."}
}

// ratioViolation describes decompression exceeding the supported expansion
// ratio.
func ratioViolation() *skillPackageError {
	return &skillPackageError{code: "SKILL_PACKAGE_EXPANSION_RATIO_LIMIT", path: skillPackageDataPath, message: "The Skill package exceeds the supported compressed-to-expanded ratio."}
}

// hasErrorFinding reports whether any validation finding blocks structural
// acceptance.
func hasErrorFinding(findings []catalog.Finding) bool {
	for _, finding := range findings {
		if finding.Severity == catalog.SeverityError {
			return true
		}
	}
	return false
}
