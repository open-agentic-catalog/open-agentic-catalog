// Package buildinfo projects bounded embedded identity without runtime Git access.
package buildinfo

import "regexp"

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// Info is compile-time identity, not startup time or an artifact attestation.
type Info struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	SourceState string `json:"sourceState"`
}

// State admits only the fixed source-state vocabulary; bare builds are unknown.
func State(value string) string {
	switch value {
	case "clean", "modified":
		return value
	default:
		return "unknown"
	}
}

// New keeps even manually linked values bounded and safe for public diagnostics.
func New(version, commit, state string) Info {
	if !identifier.MatchString(version) {
		version = "dev"
	}
	if !identifier.MatchString(commit) {
		commit = "unknown"
	}
	return Info{Version: version, Commit: commit, SourceState: State(state)}
}
