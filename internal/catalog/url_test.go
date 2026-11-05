package catalog

import "testing"

func TestExternalHTTPSURLValidation(t *testing.T) {
	for _, value := range []string{
		"https://agents.example.com/a2a",
		"https://agents.example.com:8443/a2a?profile=default",
		"https://[2001:db8::1]/a2a",
	} {
		if !ValidExternalHTTPSURL(value) {
			t.Fatalf("valid external URL %q was rejected", value)
		}
	}
	for _, value := range []string{
		"http://agents.example.com/a2a",
		"https://user:secret@agents.example.com/a2a",
		"https://agents.example.com/a2a?access_token=secret",
		"https://agents.example.com/a2a?X-Amz-Signature=secret",
		"https://agents.example.com:0/a2a",
		"https://agents.example.com/a2a#operation",
		"https://agents.example.com/a2a\nInjected: one",
		"https://ex\u00e4mple.example/a2a",
	} {
		if ValidExternalHTTPSURL(value) {
			t.Fatalf("unsafe external URL %q was accepted", value)
		}
	}
	if !ValidDocumentationHTTPSURL("https://docs.example.com/guide#configure") {
		t.Fatal("documentation fragment was rejected")
	}
	if ValidDocumentationHTTPSURL("https://docs.example.com/guide?token=secret#configure") {
		t.Fatal("credential-bearing documentation URL was accepted")
	}
}
