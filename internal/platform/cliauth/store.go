// Package cliauth acquires optional human credentials for explicitly selected
// CLI connections. It never loads runtime configuration or issues tokens.
package cliauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/oauth2"
)

const maxCredentials = 64 << 10

type credentials struct {
	Format       int       `json:"format"`
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"clientId"`
	APIOrigin    string    `json:"apiOrigin"`
	Subject      string    `json:"subject"`
	TenantID     string    `json:"tenantId"`
	MembershipID string    `json:"membershipId"`
	Nonce        string    `json:"nonce"`
	AuthTime     int64     `json:"authTime"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// A pinned private directory and a persistent advisory lock protect every
// read/replace/delete. Do not unlink the lock: that creates two lock identities.
type store struct {
	root *os.Root
	lock *os.File
	name string
}

func privateFile(file *os.File) bool {
	i, err := file.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 {
		return false
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Geteuid() && s.Nlink == 1
}

func openStore(ctx context.Context, path string) (*store, error) {
	invalid := safeerr.New("auth: credential file requires an existing owned 0700 directory and owned 0600 regular files; symlinks are refused")
	if path == "" || len(path) > 4096 {
		return nil, invalid
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, invalid
	}
	directory, name := filepath.Dir(path), filepath.Base(path)
	var directoryInfo os.FileInfo
	if name == "." || name == string(filepath.Separator) {
		return nil, invalid
	}
	// Reject symlinks in every parent, not just in the credential leaf.
	for p := directory; ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return nil, invalid
		}
		if p == directory {
			directoryInfo = i
			s, ok := i.Sys().(*syscall.Stat_t)
			if !ok || int(s.Uid) != os.Geteuid() || i.Mode().Perm() != 0700 {
				return nil, invalid
			}
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, invalid
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, actual) || actual.Mode().Perm() != 0700 {
		_ = root.Close()
		return nil, invalid
	}
	f, err := root.OpenFile(name+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		_ = root.Close()
		return nil, invalid
	}
	s := &store{root: root, lock: f, name: name}
	if !privateFile(f) {
		s.close()
		return nil, invalid
	}
	for {
		if ctx.Err() != nil {
			s.close()
			return nil, safeerr.New("auth: cancelled or deadline exceeded while waiting for credentials")
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			s.close()
			return nil, invalid
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *store) close() { _ = s.lock.Close(); _ = s.root.Close() }

func (s *store) read() (credentials, error) {
	var c credentials
	f, err := s.root.OpenFile(s.name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, os.ErrNotExist
		}
		return c, safeerr.New("auth: credential file is unavailable or unsafe")
	}
	defer func() { _ = f.Close() }()
	if !privateFile(f) {
		return c, safeerr.New("auth: credential file is not a private owned regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCredentials+1))
	if err != nil || len(b) > maxCredentials || jsondoc.Validate(b) != nil {
		return c, safeerr.New("auth: invalid credential file")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || validateCredentials(c) != nil {
		return c, safeerr.New("auth: invalid credential file")
	}
	// Re-encoding also rejects nulls and incorrectly cased known properties.
	var original, canonical any
	if json.Unmarshal(b, &original) != nil {
		return c, safeerr.New("auth: invalid credential file")
	}
	encoded, err := json.Marshal(c) // #nosec G117 -- Private credential format validation, never diagnostic output.
	if err != nil || json.Unmarshal(encoded, &canonical) != nil {
		return c, safeerr.New("auth: invalid credential file")
	}
	if !reflect.DeepEqual(original, canonical) {
		return c, safeerr.New("auth: credential fields must use the exact current format")
	}
	return c, nil
}

func (s *store) write(c credentials) error {
	if validateCredentials(c) != nil {
		return safeerr.New("auth: cannot save invalid credentials")
	}
	if _, err := s.read(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := json.Marshal(c) // #nosec G117 -- Credentials are saved only to an owned 0600 file in an owned 0700 directory.
	if err != nil || len(b) > maxCredentials {
		return safeerr.New("auth: cannot encode credentials")
	}
	name := ".catalog-login-" + oauth2.GenerateVerifier()
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return safeerr.New("auth: cannot create private credential replacement")
	}
	defer func() { _ = f.Close(); _ = s.root.Remove(name) }()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		err = s.root.Rename(name, s.name)
	}
	if err != nil {
		return safeerr.New("auth: cannot atomically save credentials; sign in again if a refresh was consumed")
	}
	return s.syncDirectory()
}

func (s *store) syncDirectory() error {
	dir, err := s.root.Open(".")
	if err != nil {
		return safeerr.New("auth: cannot sync credential directory")
	}
	defer func() { _ = dir.Close() }()
	if dir.Sync() != nil {
		return safeerr.New("auth: cannot sync credential directory; sign in again after an uncertain save")
	}
	return nil
}

// Status is local information, not proof of current issuer or Catalog access.
type Status struct {
	LoggedIn   bool       `json:"loggedIn"`
	Expired    bool       `json:"expired"`
	CanRefresh bool       `json:"canRefresh"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

func Inspect(ctx context.Context, path string) (Status, error) {
	s, err := openStore(ctx, path)
	if err != nil {
		return Status{}, err
	}
	defer s.close()
	c, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return Status{true, !c.ExpiresAt.After(time.Now()), c.RefreshToken != "", &c.ExpiresAt}, nil
}

// Logout removes only the selected local credentials, not issuer sessions.
func Logout(ctx context.Context, path string) error {
	s, err := openStore(ctx, path)
	if err != nil {
		return err
	}
	defer s.close()
	_, err = s.read()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.root.Remove(s.name) != nil {
		return safeerr.New("auth: cannot remove local credentials")
	}
	return s.syncDirectory()
}
