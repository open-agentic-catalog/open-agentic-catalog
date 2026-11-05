package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

type trustPolicyFile struct {
	Profile               string   `json:"profile"`
	RegistrationAuthority string   `json:"registrationAuthority"`
	LogOrigin             string   `json:"logOrigin"`
	EvidenceOrigin        string   `json:"evidenceOrigin"`
	PublisherDomains      []string `json:"publisherDomains"`
	RegistrationKeys      []struct {
		ID   string `json:"id"`
		File string `json:"file"`
	} `json:"registrationKeys"`
	LogKeyFiles         []string `json:"logKeyFiles"`
	MaxStatusAgeSeconds int      `json:"maxStatusAgeSeconds,omitempty"`
	FutureSkewSeconds   int      `json:"futureSkewSeconds,omitempty"`
}

// verifyTrust reads only explicitly selected local public evidence and keys.
// Runtime configuration, environment credentials and upstream URLs are unused.
func verifyTrust(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "verify-bundle" {
		return safeerr.New("usage: catalogctl trust verify-bundle --file BUNDLE --policy POLICY [--at RFC3339]")
	}
	flags := flag.NewFlagSet("catalogctl trust verify-bundle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "local evidence bundle")
	policy := flags.String("policy", "", "local public-key policy")
	at := flags.String("at", "", "explicit assessment time (RFC3339); defaults to now")
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(os.Stdout, "usage: catalogctl trust verify-bundle --file BUNDLE --policy POLICY [--at RFC3339]")
			if writeErr != nil {
				return writeErr
			}
			flags.SetOutput(os.Stdout)
			flags.PrintDefaults()
		}
		return err
	}
	if flags.NArg() != 0 || *file == "" || *policy == "" {
		return safeerr.New("file and policy are required; positional arguments are not supported")
	}
	now := time.Now().UTC()
	if *at != "" {
		var err error
		now, err = time.Parse(time.RFC3339, *at)
		if err != nil {
			return safeerr.New("assessment time must use RFC3339")
		}
	}
	v, err := loadTrustPolicy(*policy)
	if err != nil {
		return err
	}
	data, err := readTrustFile(*file, anstrust.MaxBundleBytes)
	if err != nil {
		return safeerr.New("cannot read bounded regular evidence bundle")
	}
	b, err := anstrust.DecodeBundle(data)
	if err != nil {
		return safeerr.New("invalid evidence bundle")
	}
	result, err := v.Verify(context.Background(), b, now)
	if err != nil {
		return safeerr.New("cannot assess evidence bundle")
	}
	if err := printBoundedJSON(result, 16<<10); err != nil {
		return err
	}
	if !result.CurrentRegistration {
		return safeerr.New("ANS registration verification did not establish a current matching registration; inspect result codes")
	}
	return nil
}

func loadTrustPolicy(path string) (*anstrust.Verifier, error) {
	data, err := readTrustFile(path, 64<<10)
	if err != nil {
		return nil, safeerr.New("cannot read bounded regular verification policy")
	}
	f := trustPolicyFile{FutureSkewSeconds: 60}
	if jsondoc.Decode(data, &f) != nil || len(f.RegistrationKeys) > 8 || len(f.LogKeyFiles) > 8 || f.MaxStatusAgeSeconds < 0 || f.MaxStatusAgeSeconds > 86400 || f.FutureSkewSeconds < 0 || f.FutureSkewSeconds > 60 {
		return nil, safeerr.New("invalid verification policy")
	}
	p := anstrust.Policy{Profile: f.Profile, RegistrationAuthority: f.RegistrationAuthority, LogOrigin: f.LogOrigin, EvidenceOrigin: f.EvidenceOrigin, PublisherDomains: f.PublisherDomains, RegistrationKeys: make(map[string][]byte), MaxStatusAge: time.Duration(f.MaxStatusAgeSeconds) * time.Second, FutureSkew: time.Duration(f.FutureSkewSeconds) * time.Second}
	read := func(name string) ([]byte, error) {
		if name == "" {
			return nil, anstrust.ErrPolicy
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(filepath.Dir(path), name)
		}
		return readTrustFile(name, 4096)
	}
	for _, key := range f.RegistrationKeys {
		if _, ok := p.RegistrationKeys[key.ID]; ok {
			return nil, safeerr.New("duplicate registration key identifier")
		}
		b, err := read(key.File)
		if err != nil {
			return nil, safeerr.New("cannot read bounded public verification key")
		}
		p.RegistrationKeys[key.ID] = b
	}
	for _, name := range f.LogKeyFiles {
		b, err := read(name)
		if err != nil {
			return nil, safeerr.New("cannot read bounded public verification key")
		}
		p.LogKeys = append(p.LogKeys, b)
	}
	v, err := anstrust.New(p)
	if err != nil {
		return nil, safeerr.New("invalid verification policy or public key")
	}
	return v, nil
}

func readTrustFile(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, anstrust.ErrBundle
	}
	f, err := os.Open(path) // #nosec G304 -- explicit operator-selected bounded local regular file.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, anstrust.ErrBundle
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(b) > limit {
		return nil, anstrust.ErrBundle
	}
	return b, nil
}
