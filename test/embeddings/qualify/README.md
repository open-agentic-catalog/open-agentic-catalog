# Go embedding qualifier

This test-only command uses the production embedding client against an endpoint allocated by the [parent fixture](../README.md). Run `make test-embeddings` from the repository root; do not point the command at a hosted provider. It reads a bounded fixture configuration from stdin and reports protocol assertions and elapsed time without text or vectors.

Before inference, the 600-word negative query plus its prefix must fit the configured byte ceiling; otherwise qualification fails rather than mistaking local rejection for a provider result.

Untagged input/numeric tests remain isolated. `functional`-tagged tests use an owned loopback HTTP endpoint and the real client to verify successful short controls, actual long-query rejection, accepting-provider failure and prefix-inclusive admission boundaries. They run through `make embeddings-check` and the ordinary local functional gates without a real model.
