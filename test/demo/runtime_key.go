package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
)

func prepareRuntimeKey(directory string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat("mcp-key")
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 64 {
			return errors.New("invalid existing demo client key")
		}
		raw, e := root.ReadFile("mcp-key")
		decoded, decodeErr := hex.DecodeString(string(raw))
		if e != nil || decodeErr != nil || len(decoded) != 32 {
			return errors.New("invalid existing demo client key")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	file, err := root.OpenFile("mcp-key", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(hex.EncodeToString(raw))
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
