# Public ANS evidence acquisition

This adapter uses startup-loaded named policies and the hardened `fetcher.Router`.
It never accepts credentials, arbitrary endpoints, remote keys or an alternate HTTP client.
Construction and disabled acquisition make no network calls.

`Acquire` takes one policy name, tenant and exact entry. It returns exact bytes, including a partial bundle after failure.
The caller must verify the bundle and classify acquisition errors separately.
A complete acquisition is not a positive registration assessment.
Invalid signatures stop further acquisition and remain available as negative evidence.
HTTP status errors retain only the status for later Job retry classification.

One attempt has a shared deadline, 1-4 requests and per-body plus complete JSON/base64 bounds.
An entry that cannot fit is refused before network contact and is omitted from the failed bundle.
Requests use one exact ANS receipt reference and fixed checkpoint/status paths.
An advanced log permits one exact-size history lookup. Missing history remains unavailable.
There are no redirects, source credentials, cookies, client certificates or nested retries.
The configured network profile and federation digest are fixed for the attempt.
The runtime fences current source/policy state before requests and completion.
This package owns no persistence, Job lifecycle or API.

Run ordinary units for injected request controls and native signed evidence.
Use `-tags=functional` for owned HTTPS exchanges, TLS/IP rejection, redirects, cancellation and credential isolation.
No test contacts public ANS services.

See [configuration](../../../documentation/src/content/docs/reference/configuration.mdx#ans-registration-verification),
[operator guidance](../../../documentation/src/content/docs/operations/security.md#ans-verification-authorities)
and the [offline profile](../../standards/anstrust/README.md).

The runtime connects this adapter to explicit `source.trust_verify` Jobs. A guard runs before every evidence request to check the owned lease and current source/policy generation. The adapter does not own retries or completion. See [the Job boundary](../../jobs/README.md#private-source-trust-assessments).
