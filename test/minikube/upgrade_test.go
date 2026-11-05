//go:build minikube

package main

import "testing"

func TestRequireUpgradeReplicas(t *testing.T) {
	ready := func(node string) podIdentity { return podIdentity{Node: node, Ready: true} }
	for _, test := range []struct {
		name      string
		pods      []podIdentity
		crossNode bool
		accepted  bool
	}{
		{"basic colocated", []podIdentity{ready("one"), ready("one")}, false, true},
		{"topology spread", []podIdentity{ready("one"), ready("two")}, true, true},
		{"topology colocated", []podIdentity{ready("one"), ready("one")}, true, false},
		{"first node unknown", []podIdentity{ready(""), ready("two")}, true, false},
		{"second node unknown", []podIdentity{ready("one"), ready("")}, true, false},
		{"absent", nil, true, false},
		{"one replica", []podIdentity{ready("one")}, true, false},
		{"excess replicas", []podIdentity{ready("one"), ready("two"), ready("three")}, true, false},
		{"first unready", []podIdentity{{Node: "one"}, ready("two")}, true, false},
		{"second unready", []podIdentity{ready("one"), {Node: "two"}}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := requireUpgradeReplicas(test.pods, test.crossNode); (err == nil) != test.accepted {
				t.Fatalf("replica admission = %v, want accepted=%v", err, test.accepted)
			}
		})
	}
}
