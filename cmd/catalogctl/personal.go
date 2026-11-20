// Personal commands export and restore the current human's private library through its existing API.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const personalUsage = `usage: catalogctl personal <export|preview|apply|resume> [flags]
  export: --output-dir NEW_DIRECTORY [--type resource|search|follow]
  preview: --file PATH --base-url URL --tenant-id UUID [selection]
  apply/resume: --file PATH --state-file PATH --base-url URL --confirm [selection]
  selection: --create N (repeatable, 1-based) OR --all; otherwise every item is skipped
  --base-url URL       explicit API origin (or CATALOG_REMOTE_BASE_URL)
  --token-file PATH    bearer file (or CATALOG_REMOTE_TOKEN_FILE)
                      otherwise CATALOG_REMOTE_TOKEN; no token flag
  --login-file PATH    explicit human login file, excludes bearer sources
  --issuer-ca-file PATH additional issuer roots for login-file refresh
  --ca-file PATH      additional API roots (or CATALOG_REMOTE_CA_FILE)
  --timeout DURATION whole operation, default 2m, maximum 10m
Export reads up to two 100-item pages, never targets. library.json contains
the observed items, not a point-in-time snapshot. Preview is entirely offline.
Apply and resume require explicit create/skip selection and confirmation.
Repeat the same selection on resume. Normal current permissions, target
admission and the 200-item quota apply separately to every save.
Progress uses a locked private 0600 file in a 0700 directory. Keep this file:
acknowledged saves are not replayed; uncertain saves reuse the original key
only within 23h59m of their first attempt, assuming an accurate local clock.
No automatic retries, overwrites, removals, target resolution or Search calls.
Backup and preview output contain private labels and queries. Protect them.
`

// personalCommand owns cancellation without loading runtime configuration or SQL credentials.
func personalCommand(arguments []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runPersonal(ctx, arguments, os.Getenv, os.Stdout)
}

// runPersonal keeps local preview independent of credentials and network construction.
func runPersonal(ctx context.Context, args []string, getenv func(string) string, output io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		return writeImportOutput(output, []byte(personalUsage))
	}
	action := args[0]
	if action != "export" && action != "preview" && action != "apply" && action != "resume" {
		return safeerr.New("personal: unknown command; use catalogctl personal --help")
	}
	flags := flag.NewFlagSet("personal "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options remoteOptions
	var filename, stateFile, directory, tenant, kind string
	var all, confirm bool
	var create []int
	flags.StringVar(&options.baseURL, "base-url", getenv("CATALOG_REMOTE_BASE_URL"), "explicit API origin")
	flags.DurationVar(&options.timeout, "timeout", 2*time.Minute, "whole operation deadline")
	options.maxResponseBytes = personalFileLimit
	if action == "preview" {
		flags.StringVar(&tenant, "tenant-id", "", "expected tenant UUID")
	} else {
		flags.StringVar(&options.tokenFile, "token-file", getenv("CATALOG_REMOTE_TOKEN_FILE"), "bearer file")
		flags.StringVar(&options.loginFile, "login-file", "", "explicit human login file")
		flags.StringVar(&options.issuerCAFile, "issuer-ca-file", "", "issuer roots")
		flags.StringVar(&options.caFile, "ca-file", getenv("CATALOG_REMOTE_CA_FILE"), "API roots")
	}
	if action == "export" {
		flags.StringVar(&directory, "output-dir", "", "new private directory")
		flags.StringVar(&kind, "type", "", "resource, search or follow; omitted selects all")
	} else {
		flags.StringVar(&filename, "file", "", "library JSON file")
		flags.BoolVar(&all, "all", false, "explicitly create every item")
		flags.Func("create", "1-based item index to create", func(value string) error {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > personal.MaxItems || len(create) == personal.MaxItems {
				return safeerr.New("invalid item index")
			}
			create = append(create, n)
			return nil
		})
	}
	if action == "apply" || action == "resume" {
		flags.StringVar(&stateFile, "state-file", "", "private progress file")
		flags.BoolVar(&confirm, "confirm", false, "confirm the selected creates")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeImportOutput(output, []byte(personalUsage))
		}
		return safeerr.New("personal: invalid flags; use --help")
	}
	if flags.NArg() != 0 || options.timeout <= 0 || options.timeout > 10*time.Minute {
		return safeerr.New("personal: no positional arguments; timeout must be greater than zero and at most 10m")
	}
	origin, err := remoteOrigin(options.baseURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	if action == "export" {
		if directory == "" || kind != "" && kind != "resource" && kind != "search" && kind != "follow" {
			return safeerr.New("personal: export requires a new --output-dir and an optional resource/search/follow --type")
		}
		if err = selectRemoteCredentials(&options, getenv); err != nil {
			return err
		}
		file, err := exportPersonal(ctx, options, kind)
		if err != nil {
			return err
		}
		if err = writePersonalFile(ctx, directory, file); err != nil {
			return err
		}
		return personalOutput(output, struct {
			File  string `json:"file"`
			Items int    `json:"items"`
		}{"library.json", len(file.Items)})
	}
	if filename == "" {
		return safeerr.New("personal: --file is required")
	}
	raw, file, err := readPersonalFile(filename)
	if err != nil {
		return err
	}
	selection, err := personalSelection(len(file.Items), all, create)
	if err != nil {
		return err
	}
	if origin != file.Origin || action == "preview" && (!validImportID(tenant) || tenant != file.TenantID) {
		return safeerr.New("personal: file origin or tenant does not match the selected destination; no rebinding is supported")
	}
	if action == "preview" {
		return personalOutput(output, personalPreview(file, selection))
	}
	if !confirm || stateFile == "" || !personalSeparateFiles(filename, stateFile) {
		return safeerr.New("personal: apply/resume requires --confirm and a distinct --state-file")
	}
	if err = selectRemoteCredentials(&options, getenv); err != nil {
		return err
	}
	report, applyErr := restorePersonal(ctx, options, file, raw, selection, stateFile, action == "resume")
	if err = personalOutput(output, report); err != nil {
		return err
	}
	return applyErr
}

// personalOutput bounds private machine-readable output independently of remote responses.
func personalOutput(output io.Writer, value any) error {
	raw, err := remoteJSON(value)
	if err != nil || len(raw)+1 > 16<<20 {
		return safeerr.New("personal: output exceeds its 16 MiB terminal-safe limit; preserve existing progress")
	}
	return writeImportOutput(output, append(raw, '\n'))
}

// personalSelection requires deliberate creates; duplicate entries remain independent choices.
func personalSelection(count int, all bool, create []int) ([]bool, error) {
	selected := make([]bool, count)
	if all && len(create) != 0 {
		return nil, safeerr.New("personal: choose --all or repeat --create, not both")
	}
	for i := range selected {
		selected[i] = all
	}
	for _, index := range create {
		if index < 1 || index > count || selected[index-1] {
			return nil, safeerr.New("personal: create indexes must be distinct and present in the file")
		}
		selected[index-1] = true
	}
	return selected, nil
}

// personalPreview projects exact intent without reads, saves or target resolution.
func personalPreview(file personalFile, selected []bool) any {
	type row struct {
		Index  int            `json:"index"`
		Action string         `json:"action"`
		Item   personal.Input `json:"item"`
	}
	rows := make([]row, len(file.Items))
	for i, item := range file.Items {
		action := "skip"
		if selected[i] {
			action = "create"
		}
		rows[i] = row{i + 1, action, item}
	}
	return struct {
		Origin      string `json:"origin"`
		TenantID    string `json:"tenantId"`
		Consistency string `json:"consistency"`
		Items       []row  `json:"items"`
	}{file.Origin, file.TenantID, file.Consistency, rows}
}
