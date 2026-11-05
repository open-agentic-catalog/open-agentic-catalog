# Local embedding profiles

[profiles/](profiles/README.md) contains checksum-pinned CPU TEI model inputs for local qualification and the [relevance comparison](../../test/search-relevance/comparison-v2/README.md). These are test profiles, not a runtime model allowlist. The application accepts one configured OpenAI-compatible endpoint. Relevance limitations remain separate from functional correctness.

Use `make search-relevance-compare-install` to acquire both profiles explicitly, then `make search-relevance-compare` to evaluate them offline. The shared launcher is maintained in `test/search-relevance/runner.py`. `make embeddings-install` acquires only the selected profile; `make compose-up LOCAL_EMBEDDINGS=on` adds its offline service without enabling runtime retrieval or changing the database. See [hybrid search operations](../../documentation/src/content/docs/operations/hybrid-search.mdx) for explicit setup and Helm endpoint configuration.

For a single-model Go client check, use `make embeddings-install`, then `make test-embeddings`. These default to MiniLM and verify the actual Catalog embedding adapter against an owned offline TEI container, without a Catalog database. `make embeddings-check` is service-free. See the [endpoint harness](../../test/embeddings/README.md) for selection, prerequisites and cleanup; these checks do not enable hybrid Search.

Only selected model files enter ignored local caches. No model weights or Python runtime are included in Catalog images. Server/model pins, licences and preprocessing must be reviewed together before adding a new versioned profile; do not silently replace weights or tune using an inspected held-out set.
