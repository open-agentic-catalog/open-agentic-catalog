package fetcher

import (
	"net/netip"
	"testing"
)

func TestPublicReferenceAdmissionWithoutNetwork(t *testing.T) {
	for _, value := range []string{"https://images.example/icon.png", "https://8.8.8.8/icon.png", "https://[2606:4700:4700::1111]/icon.png"} {
		if !PublicReferenceAllowed(value, nil) {
			t.Fatalf("public reference denied: %s", value)
		}
	}
	for _, value := range []string{"http://images.example/icon.png", "https://127.0.0.1/icon.png", "https://10.0.0.1/icon.png", "https://192.168.1.2/icon.png", "https://[fc00::1]/icon.png", "https://[::1]/icon.png", "https://169.254.169.254/icon.png", "https://100.64.0.1/icon.png", "https://192.0.2.1/icon.png", "https://[2001:db8::1]/icon.png", "https://user:secret@images.example/icon.png", "https://images.example/icon.png?token=secret"} {
		if PublicReferenceAllowed(value, nil) {
			t.Fatalf("private/unsafe reference admitted: %s", value)
		}
	}
	if PublicReferenceAllowed("https://8.8.8.8/icon.png", []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}) {
		t.Fatal("operator address denial ignored")
	}
}
