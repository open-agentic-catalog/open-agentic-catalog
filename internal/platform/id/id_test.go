package id

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestUUIDv7LayoutAndOrder(t *testing.T) {
	g := Generator{Reader: bytes.NewReader(make([]byte, 64))}
	a, err := g.New(time.UnixMilli(1_700_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.New(time.UnixMilli(1_700_000_000_001))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 36 || a[14] != '7' || a[19] != '8' {
		t.Fatalf("invalid UUIDv7 layout: %q", a)
	}
	if a >= b {
		t.Fatalf("UUIDv7 values are not time ordered: %q >= %q", a, b)
	}
}

func TestUUIDv7RejectsEntropyAndTimestampFailures(t *testing.T) {
	generator := Generator{Reader: bytes.NewReader(nil)}
	if _, err := generator.New(time.UnixMilli(0)); err == nil {
		t.Fatal("short entropy source was accepted")
	}
	generator.Reader = bytes.NewReader(make([]byte, 32))
	for _, instant := range []time.Time{time.UnixMilli(-1), time.UnixMilli(1 << 48)} {
		if _, err := generator.New(instant); err == nil {
			t.Fatalf("out-of-range timestamp %v was accepted", instant)
		}
	}
	if _, err := (Generator{Reader: errorReader{}}).New(time.UnixMilli(0)); err == nil {
		t.Fatal("entropy read failure was accepted")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
