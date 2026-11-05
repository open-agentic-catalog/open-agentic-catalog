//go:build minikube

// Rotation identities must reject unchanged or incomplete two-replica evidence.
package main

import "testing"

func TestUIRotationPodsReplaced(t *testing.T) {
	before := []podIdentity{{UID: "old-a", Ready: true}, {UID: "old-b", Ready: true}}
	after := []podIdentity{{UID: "new-a", Ready: true}, {UID: "new-b", Ready: true}}
	if !uiRotationPodsReplaced(before, after) {
		t.Fatal("valid replacement rejected")
	}
	for _, bad := range [][]podIdentity{
		nil, after[:1], {{UID: "old-b", Ready: true}, {UID: "new-b", Ready: true}},
		{{UID: "new-a", Ready: true}, {UID: "new-a", Ready: true}},
		{{UID: "", Ready: true}, {UID: "new-b", Ready: true}},
		{{UID: "new-a", Ready: false}, {UID: "new-b", Ready: true}},
	} {
		if uiRotationPodsReplaced(before, bad) {
			t.Fatal("incomplete replacement admitted")
		}
	}
}
