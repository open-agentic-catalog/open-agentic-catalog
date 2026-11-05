// Feedback audit records retain only state and revision, never private text.
package audit

// feedbackFacts is deliberately closed; comments and responses are not evidence.
func feedbackFacts() map[string]factRule {
	return map[string]factRule{"revision": requiredFact(factInteger), "state": requiredFact(factText)}
}
