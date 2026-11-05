package catalog

import (
	"strings"
	"testing"
)

func TestValidSkillNames(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"a", "skill-2", strings.Repeat("a", 64)} {
		if !ValidSkillName(value) {
			t.Errorf("valid Skill name %q was rejected", value)
		}
	}
	for _, value := range []string{"", "-skill", "skill-", "skill--name", "Skill", "skill_name", strings.Repeat("a", 65), string([]byte{0xff})} {
		if ValidSkillName(value) {
			t.Errorf("invalid Skill name %q was accepted", value)
		}
	}
}

func TestSkillHandoffNormalization(t *testing.T) {
	t.Parallel()
	handoff, err := NormalizeSkillHandoff(SkillHandoff{
		Git: &GitHandoffReference{
			Repository: " HTTPS://Git.Example.com/team/skills.git/ ",
			Commit:     strings.Repeat("A", 40),
			Directory:  "catalog/skills/safe-skill",
		},
		OCI: &OCIHandoffReference{Reference: "REGISTRY.EXAMPLE.COM/team/safe-skill@sha256:" + strings.Repeat("b", 64)},
	}, "safe-skill")
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Git.Repository != "https://git.example.com/team/skills.git" || handoff.Git.Commit != strings.Repeat("a", 40) || handoff.Git.Directory != "catalog/skills/safe-skill" {
		t.Fatalf("normalized Git handoff = %#v", handoff.Git)
	}
	if handoff.OCI.Reference != "registry.example.com/team/safe-skill@sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("normalized OCI handoff = %#v", handoff.OCI)
	}
	for _, reference := range []string{
		"localhost/team/safe-skill@sha256:" + strings.Repeat("b", 64),
		"registry:5000/team/safe-skill@sha256:" + strings.Repeat("b", 64),
		"127.0.0.1/team/safe-skill@sha256:" + strings.Repeat("b", 64),
		"[2001:db8::1]/team/safe-skill@sha256:" + strings.Repeat("b", 64),
	} {
		if _, err := NormalizeSkillHandoff(SkillHandoff{OCI: &OCIHandoffReference{Reference: reference}}, "safe-skill"); err != nil {
			t.Errorf("explicit OCI registry %q was rejected: %v", reference, err)
		}
	}
}

func TestSkillHandoffEqualityDistinguishesReferenceKindsAndValues(t *testing.T) {
	t.Parallel()

	git := GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/example"}
	otherGit := git
	otherGit.Directory = "skills/other"
	oci := OCIHandoffReference{Reference: "registry.example.com/team/skill@sha256:" + strings.Repeat("b", 64)}
	otherOCI := OCIHandoffReference{Reference: "registry.example.com/team/skill@sha256:" + strings.Repeat("c", 64)}
	for name, test := range map[string]struct {
		left  SkillHandoff
		right SkillHandoff
		want  bool
	}{
		"empty":               {want: true},
		"equal Git":           {left: SkillHandoff{Git: &git}, right: SkillHandoff{Git: &git}, want: true},
		"different Git":       {left: SkillHandoff{Git: &git}, right: SkillHandoff{Git: &otherGit}},
		"missing Git":         {left: SkillHandoff{Git: &git}},
		"equal OCI":           {left: SkillHandoff{OCI: &oci}, right: SkillHandoff{OCI: &oci}, want: true},
		"different OCI":       {left: SkillHandoff{OCI: &oci}, right: SkillHandoff{OCI: &otherOCI}},
		"different reference": {left: SkillHandoff{Git: &git}, right: SkillHandoff{OCI: &oci}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sameSkillHandoff(test.left, test.right); got != test.want {
				t.Fatalf("sameSkillHandoff() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestSkillHandoffRejectsMutableOrUnsafeReferences(t *testing.T) {
	t.Parallel()
	validGit := GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("a", 40), Directory: "skills/safe-skill"}
	validOCI := OCIHandoffReference{Reference: "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)}
	tests := map[string]SkillHandoff{
		"missing":               {},
		"Git HTTP":              {Git: &GitHandoffReference{Repository: "http://git.example.com/team/repo", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git credentials":       {Git: &GitHandoffReference{Repository: strings.Join([]string{"https://user", "credential@git.example.com/team/repo"}, ":"), Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git query":             {Git: &GitHandoffReference{Repository: "https://git.example.com/team/repo?token=x", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git empty query":       {Git: &GitHandoffReference{Repository: "https://git.example.com/team/repo?", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git fragment":          {Git: &GitHandoffReference{Repository: "https://git.example.com/team/repo#main", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git empty fragment":    {Git: &GitHandoffReference{Repository: "https://git.example.com/team/repo#", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git empty path":        {Git: &GitHandoffReference{Repository: "https://git.example.com", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git encoded path":      {Git: &GitHandoffReference{Repository: "https://git.example.com/team/%72epo", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git dot path":          {Git: &GitHandoffReference{Repository: "https://git.example.com/team/./repo", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git parent path":       {Git: &GitHandoffReference{Repository: "https://git.example.com/team/../repo", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git empty segment":     {Git: &GitHandoffReference{Repository: "https://git.example.com/team//repo", Commit: validGit.Commit, Directory: validGit.Directory}},
		"Git short commit":      {Git: &GitHandoffReference{Repository: validGit.Repository, Commit: "main", Directory: validGit.Directory}},
		"Git traversal":         {Git: &GitHandoffReference{Repository: validGit.Repository, Commit: validGit.Commit, Directory: "../safe-skill"}},
		"Git root mismatch":     {Git: &validGit},
		"OCI tag":               {OCI: &OCIHandoffReference{Reference: "registry.example.com/team/safe-skill:latest"}},
		"OCI missing registry":  {OCI: &OCIHandoffReference{Reference: "safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI implicit registry": {OCI: &OCIHandoffReference{Reference: "team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI invalid hostname":  {OCI: &OCIHandoffReference{Reference: "-registry.example.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI trailing dot":      {OCI: &OCIHandoffReference{Reference: "registry.example.com./team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI upper repository":  {OCI: &OCIHandoffReference{Reference: "registry.example.com/Team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI short digest":      {OCI: &OCIHandoffReference{Reference: "registry.example.com/team/safe-skill@sha256:abcd"}},
		"OCI URL":               {OCI: &OCIHandoffReference{Reference: "https://registry.example.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI multiple at":       {OCI: &OCIHandoffReference{Reference: "registry.example.com/team@name/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI empty repository":  {OCI: &OCIHandoffReference{Reference: "registry.example.com/@sha256:" + strings.Repeat("b", 64)}},
		"OCI zero port":         {OCI: &OCIHandoffReference{Reference: "registry.example.com:0/team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI empty port":        {OCI: &OCIHandoffReference{Reference: "registry.example.com:/team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI invalid port":      {OCI: &OCIHandoffReference{Reference: "registry.example.com:bad/team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI Unicode host":      {OCI: &OCIHandoffReference{Reference: "registry.exämple.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)}},
		"OCI mutable tag":       {OCI: &OCIHandoffReference{Reference: "registry.example.com/team/safe-skill:latest@sha256:" + strings.Repeat("b", 64)}},
		"OCI path gap":          {OCI: &OCIHandoffReference{Reference: "registry.example.com/team//safe-skill@sha256:" + strings.Repeat("b", 64)}},
	}
	for name, handoff := range tests {
		t.Run(name, func(t *testing.T) {
			expectedRoot := ""
			if name == "Git root mismatch" {
				expectedRoot = "other-skill"
			}
			if _, err := NormalizeSkillHandoff(handoff, expectedRoot); err == nil {
				t.Fatalf("unsafe handoff was accepted: %#v", handoff)
			}
		})
	}
	if _, err := NormalizeSkillHandoff(SkillHandoff{OCI: &validOCI}, "safe-skill"); err != nil {
		t.Fatalf("valid OCI reference was rejected: %v", err)
	}
}

func TestSkillPackagePathNormalization(t *testing.T) {
	t.Parallel()
	if value, ok := NormalizeSkillPackagePath("safe-skill/references/REFERENCE.md"); !ok || value != "safe-skill/references/REFERENCE.md" {
		t.Fatalf("normalized path = %q, %t", value, ok)
	}
	for _, value := range []string{"", "/safe-skill/SKILL.md", "../safe-skill/SKILL.md", "safe-skill/references/../SKILL.md", "safe-skill/./SKILL.md", "safe-skill//SKILL.md", `safe-skill\SKILL.md`, "safe-skill/C:/file", "safe-skill/file.", "safe-skill/file ", "safe-skill/file\nname", "safe-skill/\x00file", string([]byte{'s', 'a', 'f', 'e', '/', 0xff}), "safe-skill/" + strings.Repeat("a", 256), strings.Repeat("a", MaxSkillPackagePathBytes+1)} {
		if normalized, ok := NormalizeSkillPackagePath(value); ok {
			t.Fatalf("unsafe path %q normalized to %q", value, normalized)
		}
	}
	deep := "safe-skill/" + strings.Repeat("directory/", MaxSkillPackageDepth) + "file"
	if _, ok := NormalizeSkillPackagePath(deep); ok {
		t.Fatal("over-deep package path was accepted")
	}
}

func TestRepositorySkillPackageMetadataValidation(t *testing.T) {
	t.Parallel()
	if err := ValidatePackageRepresentation(Representation{}); err != nil {
		t.Fatalf("ordinary representation returned %v", err)
	}
	withoutPackage := Representation{Handoff: &SkillHandoff{OCI: &OCIHandoffReference{Reference: "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("a", 64)}}}
	if err := ValidatePackageRepresentation(withoutPackage); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("handoff without package returned %v", err)
	}
	representation := validSkillPackageRepresentation()
	if err := ValidatePackageRepresentation(representation); err != nil {
		t.Fatalf("valid package representation returned %v", err)
	}
	tests := map[string]func(*Representation){
		"media type": func(value *Representation) { value.MediaType = "application/zip" },
		"delivery":   func(value *Representation) { value.URL = "https://example.com/package.zip" },
		"data":       func(value *Representation) { value.Data = []byte(`{}`) },
		"artifact":   func(value *Representation) { value.ArtifactID = "artifact" },
		"digest":     func(value *Representation) { value.ContentDigest = "sha256:bad" },
		"size zero":  func(value *Representation) { value.Size = 0 },
		"size limit": func(value *Representation) { value.Size = MaxSkillPackageBytes + 1 },
		"handoff":    func(value *Representation) { value.Handoff = nil },
		"root mismatch": func(value *Representation) {
			value.Package.RootDirectory = "other-skill"
		},
		"handoff normalization": func(value *Representation) {
			value.Handoff.Git.Repository = "https://GIT.EXAMPLE.COM/team/skills.git"
		},
		"structure state": func(value *Representation) {
			value.ValidationState = "invalid"
			value.Package.StructureState = PackageAssessmentUnavailable
		},
		"invalid root": func(value *Representation) {
			value.ValidationState = "invalid"
			value.Package.RootDirectory = "Unsafe"
		},
		"count":    func(value *Representation) { value.Package.FileCount++ },
		"expanded": func(value *Representation) { value.Package.ExpandedSize++ },
		"zero expanded": func(value *Representation) {
			value.Package.Files[0].Size = 0
			value.Package.ExpandedSize = 0
		},
		"expansion ratio": func(value *Representation) {
			value.Size = 1
			value.Package.Files[0].Size = MaxSkillPackageExpansionRatio + 1
			value.Package.ExpandedSize = MaxSkillPackageExpansionRatio + 1
		},
		"file path": func(value *Representation) {
			value.Package.Files[0].Path = "../SKILL.md"
		},
		"file order": func(value *Representation) {
			value.Package.Files = []SkillPackageFile{
				{Path: "safe-skill/z.txt", Size: 1, MediaType: "text/plain", Digest: "sha256:" + strings.Repeat("d", 64)},
				{Path: "safe-skill/SKILL.md", Size: 10, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("b", 64)},
			}
			value.Package.FileCount = 2
			value.Package.ExpandedSize = 11
		},
		"case-conflicting files": func(value *Representation) {
			value.Package.Files = []SkillPackageFile{
				{Path: "safe-skill/README.md", Size: 1, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("d", 64)},
				{Path: "safe-skill/SKILL.md", Size: 10, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("b", 64)},
				{Path: "safe-skill/readme.md", Size: 1, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("e", 64)},
			}
			value.Package.FileCount = 3
			value.Package.ExpandedSize = 12
		},
		"file used as parent": func(value *Representation) {
			value.Package.Files = []SkillPackageFile{
				{Path: "safe-skill/SKILL.md", Size: 10, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("b", 64)},
				{Path: "safe-skill/references", Size: 1, MediaType: "application/octet-stream", Digest: "sha256:" + strings.Repeat("d", 64)},
				{Path: "safe-skill/references/README.md", Size: 1, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("e", 64)},
			}
			value.Package.FileCount = 3
			value.Package.ExpandedSize = 12
		},
		"file size": func(value *Representation) { value.Package.Files[0].Size = -1 },
		"file digest": func(value *Representation) {
			value.Package.Files[0].Digest = "bad"
		},
		"file media type": func(value *Representation) {
			value.Package.Files[0].MediaType = "invalid media type"
		},
		"missing SKILL": func(value *Representation) {
			value.Package.Files[0].Path = "safe-skill/README.md"
		},
		"expanded limit": func(value *Representation) {
			value.Package.Files = make([]SkillPackageFile, 5)
			for index := range value.Package.Files {
				value.Package.Files[index] = SkillPackageFile{Path: "safe-skill/" + string(rune('a'+index)) + ".txt", Size: MaxSkillPackageFileBytes, MediaType: "text/plain", Digest: "sha256:" + strings.Repeat("e", 64)}
			}
			value.Package.Files[0].Path = "safe-skill/SKILL.md"
			value.Package.FileCount = len(value.Package.Files)
			value.Package.ExpandedSize = 5 * MaxSkillPackageFileBytes
		},
		"assessment": func(value *Representation) { value.Package.Assessments.Malware = "unknown" },
		"failed assessment": func(value *Representation) {
			value.Package.Assessments.Malware = PackageAssessmentFailed
		},
		"failed inventory": func(value *Representation) { value.Package.StructureState = PackageAssessmentFailed },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := cloneSkillPackageRepresentation(representation)
			mutate(&value)
			if err := ValidatePackageRepresentation(value); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("invalid package returned %v", err)
			}
		})
	}
	failed := cloneSkillPackageRepresentation(representation)
	failed.ValidationState = "invalid"
	failed.Package = &SkillPackageInspection{StructureState: PackageAssessmentFailed, Assessments: NotAssessedSkillPackageAssessments()}
	if err := ValidatePackageRepresentation(failed); err != nil {
		t.Fatalf("honest failed inspection was rejected: %v", err)
	}
	withOCI := cloneSkillPackageRepresentation(representation)
	withOCI.Handoff = &SkillHandoff{OCI: &OCIHandoffReference{Reference: "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("f", 64)}}
	if err := ValidatePackageRepresentation(withOCI); err != nil {
		t.Fatalf("valid OCI handoff returned %v", err)
	}
}

func validSkillPackageRepresentation() Representation {
	return Representation{
		MediaType:       SkillPackageMediaZIP,
		ContentDigest:   "sha256:" + strings.Repeat("a", 64),
		Size:            100,
		ValidationState: "valid",
		Package: &SkillPackageInspection{
			StructureState: PackageAssessmentPassed,
			RootDirectory:  "safe-skill",
			ExpandedSize:   10,
			FileCount:      1,
			Files:          []SkillPackageFile{{Path: "safe-skill/SKILL.md", Size: 10, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("b", 64)}},
			Assessments:    NotAssessedSkillPackageAssessments(),
		},
		Handoff: &SkillHandoff{Git: &GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("c", 40), Directory: "skills/safe-skill"}},
	}
}

func cloneSkillPackageRepresentation(value Representation) Representation {
	result := value
	packageCopy := *value.Package
	packageCopy.Files = append([]SkillPackageFile(nil), value.Package.Files...)
	result.Package = &packageCopy
	handoffCopy := *value.Handoff
	if value.Handoff.Git != nil {
		gitCopy := *value.Handoff.Git
		handoffCopy.Git = &gitCopy
	}
	if value.Handoff.OCI != nil {
		ociCopy := *value.Handoff.OCI
		handoffCopy.OCI = &ociCopy
	}
	result.Handoff = &handoffCopy
	return result
}
