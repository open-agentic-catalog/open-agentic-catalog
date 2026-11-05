package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const importMaxReceiptBytes = 2 << 20

type importPrincipal struct {
	TenantID     string `json:"tenantId"`
	RealmID      string `json:"realmId"`
	MembershipID string `json:"membershipId"`
	Subject      string `json:"subject"`
}

type importAcknowledgement struct {
	Findings   []catalog.Finding `json:"findings,omitempty"`
	ID         string            `json:"id"`
	RevisionID string            `json:"revisionId,omitempty"`
	EvidenceID string            `json:"evidenceId,omitempty"`
	Digest     string            `json:"digest,omitempty"`
	ETag       string            `json:"etag,omitempty"`
	Blocked    bool              `json:"blocked,omitempty"`
}

type importStep struct {
	State        string                `json:"state"`
	Key          string                `json:"key"`
	Method       string                `json:"method"`
	Path         string                `json:"path,omitempty"`
	BodyDigest   string                `json:"bodyDigest,omitempty"`
	IfMatch      string                `json:"ifMatch,omitempty"`
	FirstAttempt time.Time             `json:"firstAttempt,omitzero"`
	RequestID    string                `json:"requestId,omitempty"`
	Ack          importAcknowledgement `json:"ack"`
}

type importReceipt struct {
	Format       int                 `json:"format"`
	Origin       string              `json:"origin"`
	Principal    importPrincipal     `json:"principal"`
	IntentDigest string              `json:"intentDigest"`
	Steps        []importStep        `json:"steps"`
	Draft        *importDraftBinding `json:"draft,omitempty"`
}

// The checksum detects accidental edits/corruption, not an attacker with the
// same OS account. Private local files are the receipt's authority boundary.
type importReceiptFile struct {
	Receipt importReceipt `json:"receipt"`
	Digest  string        `json:"digest"`
}

type importStateFile struct {
	root     *os.Root
	name     string
	lock     *os.File
	previous []byte
}

func importIntent(plan importBatchPlan) string {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return ""
	}
	return importDigest(encoded)
}

func newImportReceipt(plan importBatchPlan, origin string, principal importPrincipal) (importReceipt, error) {
	r := importReceipt{Format: 2, Origin: origin, Principal: principal, IntentDigest: importIntent(plan)}
	return createImportSteps(r, len(plan.Items)*importStepsPerResource)
}

func createImportSteps(r importReceipt, count int) (importReceipt, error) {
	if !importDigestValid(r.IntentDigest) {
		return r, safeerr.New("import: cannot bind prepared intent")
	}
	for range count {
		var key [24]byte
		if _, err := rand.Read(key[:]); err != nil {
			return r, safeerr.New("import: cannot generate command keys")
		}
		r.Steps = append(r.Steps, importStep{State: "ready", Method: http.MethodPost, Key: "import-" + hex.EncodeToString(key[:])})
	}
	return r, nil
}

func openImportState(filename string, resume bool) (*importStateFile, error) {
	fail := safeerr.New("import: state requires a private directory and regular files, without symlinks or another active invocation")
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, fail
	}
	parent := filepath.Dir(abs)
	if !resume {
		if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
			return nil, fail
		}
	}
	// Reject ancestor symlinks too; os.Root confines later relative operations.
	for p := parent; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fail
		}
		if p == parent && info.Mode().Perm()&0077 != 0 {
			return nil, fail
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, fail
	}
	f := &importStateFile{root: root, name: filepath.Base(abs)}
	if f.name == "." || strings.HasSuffix(f.name, ".lock") {
		_ = root.Close()
		return nil, fail
	}
	// Root.OpenFile confines links but can follow them even with O_NOFOLLOW.
	// Inspect the entry itself and compare it with the opened file below.
	if info, err := root.Lstat(f.name + ".lock"); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			f.close()
			return nil, fail
		}
	} else if !os.IsNotExist(err) {
		f.close()
		return nil, fail
	}
	lock, err := root.OpenFile(f.name+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		_ = root.Close()
		return nil, fail
	}
	f.lock = lock
	info, err := lock.Stat()
	actual, statErr := root.Lstat(f.name + ".lock")
	if err != nil || statErr != nil || !actual.Mode().IsRegular() || !os.SameFile(actual, info) || info.Mode().Perm() != 0600 || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.close()
		return nil, fail
	}
	if resume {
		f.previous, err = f.read()
		if err != nil {
			f.close()
			return nil, err
		}
	} else if _, err := root.Lstat(f.name); !os.IsNotExist(err) {
		f.close()
		return nil, safeerr.New("import: apply requires a new state file; use resume for existing progress")
	}
	return f, nil
}

func (f *importStateFile) close() {
	if f.lock != nil {
		_ = f.lock.Close()
	}
	_ = f.root.Close()
}

func (f *importStateFile) read() ([]byte, error) {
	fail := safeerr.New("import: cannot read private bounded state file")
	expected, err := f.root.Lstat(f.name)
	if err != nil || !expected.Mode().IsRegular() || expected.Mode().Perm() != 0600 || expected.Size() > importMaxReceiptBytes {
		return nil, fail
	}
	file, err := f.root.OpenFile(f.name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fail
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	actual, statErr := f.root.Lstat(f.name)
	if err != nil || statErr != nil || !actual.Mode().IsRegular() || !os.SameFile(expected, info) || !os.SameFile(actual, info) || info.Mode().Perm() != 0600 || info.Size() > importMaxReceiptBytes {
		return nil, fail
	}
	raw, err := io.ReadAll(io.LimitReader(file, importMaxReceiptBytes+1))
	if err != nil || len(raw) > importMaxReceiptBytes {
		return nil, fail
	}
	return raw, nil
}

func decodeImportReceipt(raw []byte) (importReceipt, error) {
	var file importReceiptFile
	fail := safeerr.New("import: state is invalid or changed; preserve it for investigation")
	if len(raw) > importMaxReceiptBytes || jsondoc.Decode(raw, &file) != nil {
		return file.Receipt, fail
	}
	encoded, err := json.Marshal(file.Receipt)
	if err != nil || file.Digest != importDigest(encoded) || !validImportReceiptShape(file.Receipt) || !importDigestValid(file.Receipt.IntentDigest) {
		return file.Receipt, fail
	}
	seen := map[string]bool{}
	unacknowledged := false
	for _, step := range file.Receipt.Steps {
		if len(step.Key) != 55 || !strings.HasPrefix(step.Key, "import-") || step.Method != http.MethodPost || seen[step.Key] {
			return file.Receipt, fail
		}
		if _, err := hex.DecodeString(step.Key[7:]); err != nil {
			return file.Receipt, fail
		}
		seen[step.Key] = true
		if unacknowledged && step.State != "ready" {
			return file.Receipt, fail
		}
		switch step.State {
		case "ready":
			if step.Path != "" || step.BodyDigest != "" || step.IfMatch != "" || step.RequestID != "" || !step.FirstAttempt.IsZero() || !reflect.DeepEqual(step.Ack, importAcknowledgement{}) {
				return file.Receipt, fail
			}
		case "inflight", "uncertain", "failed", "acknowledged":
			if step.Path == "" || step.BodyDigest == "" || step.FirstAttempt.IsZero() {
				return file.Receipt, fail
			}
		default:
			return file.Receipt, fail
		}
		if step.State == "acknowledged" {
			if unacknowledged || !validImportID(step.Ack.ID) {
				return file.Receipt, fail
			}
		} else {
			unacknowledged = true
		}
	}
	return file.Receipt, nil
}

func (f *importStateFile) save(receipt importReceipt) error {
	fail := safeerr.New("import: cannot durably save state; preserve drafts and investigate before retrying")
	if f.previous != nil {
		current, err := f.read()
		if err != nil || !bytes.Equal(current, f.previous) {
			return fail
		}
	} else if _, err := f.root.Lstat(f.name); !os.IsNotExist(err) {
		return fail
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fail
	}
	raw, err := json.Marshal(importReceiptFile{Receipt: receipt, Digest: importDigest(encoded)})
	if err != nil || len(raw) > importMaxReceiptBytes {
		return fail
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fail
	}
	name := f.name + ".tmp-" + hex.EncodeToString(suffix[:])
	temp, err := f.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fail
	}
	defer func() { _ = temp.Close(); _ = f.root.Remove(name) }()
	if n, err := temp.Write(raw); err != nil || n != len(raw) {
		return fail
	}
	if temp.Sync() != nil || temp.Close() != nil {
		return fail
	}
	if f.root.Rename(name, f.name) != nil {
		return fail
	}
	dir, err := f.root.Open(".")
	if err != nil {
		return fail
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return fail
	}
	f.previous = raw
	return nil
}

// The server retains completed commands for 24 hours. The conservative margin
// avoids claiming exactness near its boundary; local wall-clock accuracy is an
// operator assumption. A missing/future timestamp is never permission to send.
func importReplayAllowed(first, now time.Time) bool {
	return !first.IsZero() && !now.Before(first) && now.Sub(first) < 24*time.Hour-time.Minute
}
