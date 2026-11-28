package main

import (
	"io"
	"strings"
	"testing"
)

func TestScannerBufferBoundsWhileCopying(t *testing.T) {
	buffer := &boundedBuffer{limit: 8}
	// Expose only Read so io.Copy uses the same destination methods as a pipe.
	copyText := func(s string) (int64, error) {
		return io.Copy(buffer, io.LimitReader(strings.NewReader(s), int64(len(s))))
	}
	for _, part := range []string{"abc", "defgh"} {
		if n, err := copyText(part); err != nil || n != int64(len(part)) || buffer.overflow {
			t.Fatalf("copy %q: accepted=%d overflow=%t err=%v", part, n, buffer.overflow, err)
		}
	}
	if n, err := copyText("!"); err == nil || n != 0 || !buffer.overflow || buffer.buf.String() != "abcdefgh" {
		t.Fatalf("extra byte: accepted=%d retained=%q overflow=%t err=%v", n, buffer.buf.String(), buffer.overflow, err)
	}
}
