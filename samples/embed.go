// Package samples embeds the same checked-in dataset used by local Make commands.
package samples

import "embed"

// Files is the default manifest and its native package assets, with no Go-authored inventory.
//
//go:embed catalog.yaml cards packages upstream first-party artwork
var Files embed.FS
