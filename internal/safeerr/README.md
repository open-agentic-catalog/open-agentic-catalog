# Reviewed error projections

`New` marks a fixed reviewed message safe for terminal output. `Wrap` retains an internal cause and assigns separate reviewed guidance; its `Error` and `Unwrap` methods preserve internal diagnostics and classification. Neither constructor may receive dependency text, user input, URLs, private paths or formatted secret values. `Message` selects only explicitly reviewed text and otherwise returns a fixed fallback. `At` and `Stage` carry code-owned operation labels. The remote CLI separately preserves its existing bounded JSON error contract.

`Recover` is deferred directly at an owned execution boundary. It discards the panic value and returns `ErrPanic`; the owner must cancel, drain and stop rather than retry or resume work. It does not log, start goroutines or claim control over dependency goroutines, initialization or fatal runtime failures. See the [observability runbook](../../documentation/src/content/docs/operations/observability.mdx#current-safety-and-interpretation-limits).

The package depends only on the standard library. [Platform diagnostics](../platform/diagnostics/README.md) owns command formatting and HTTP handling; domains use only the failure classification and containment helper.
