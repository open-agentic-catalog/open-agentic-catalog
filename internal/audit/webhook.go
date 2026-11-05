// Webhook endpoint actions share required revision and secret-version facts.

package audit

// webhookEndpointFacts defines the shared endpoint revision, secret-version and
// status facts with optional predecessor revocation.
func webhookEndpointFacts() map[string]factRule {
	return map[string]factRule{"revision": requiredFact(factInteger), "secretVersion": requiredFact(factInteger), "status": requiredFact(factText), "revokePrevious": optionalFact(factBoolean)}
}
