# Qualification profiles

| Profile | Model | Input policy | License |
| --- | --- | --- | --- |
| `bge-small-v1.json` | BAAI/bge-small-en-v1.5 | 384 dimensions, CLS pooling, unit normalization, query-only instruction, 512 tokens | MIT |
| `minilm-l6-v1.json` | sentence-transformers/all-MiniLM-L6-v2 | 384 dimensions, mean pooling, unit normalization, no instruction, 256 tokens | Apache-2.0 |
| `granite-278m-v1.json` | ibm-granite/granite-embedding-278m-multilingual | 768 dimensions, CLS pooling, unit normalization, no instruction, 512 tokens | Apache-2.0 |
| `multilingual-e5-large-v1.json` | intfloat/multilingual-e5-large | 1024 dimensions, mean pooling, unit normalization, query and passage prefixes, 512 tokens | MIT |

All use pinned CPU TEI 1.9.4 (Apache-2.0). JSON manifests retain SHA-256 file hashes and immutable model and image revisions. Model cards remain in the downloaded caches with their license declarations; they are not a license for other models. Profiles use separate pinned manifests and caches. The benchmark experiment file owns its cutoff grid and acceptance criteria; it overrides those evaluation fields without changing the model preprocessing.

MiniLM's [pinned model card](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/blob/1110a243fdf4706b3f48f1d95db1a4f5529b4d41/README.md) specifies mean pooling and the default 256-word-piece limit. Its pinned `sentence_bert_config.json` supplies that limit to TEI; a real 266-word request must be rejected rather than silently truncated. BGE uses its original [pinned profile](../../../test/search-relevance/manifest.json). Query prefixes are applied by the harness, never as a server-wide default affecting document embeddings.

These manifests define local benchmark candidates. Run the explicit [real API/agent campaign](../../../test/search-relevance/benchmark/README.md) to qualify serving behavior and measure quality. Equal dimensions do not make their vectors interchangeable, and HTTP compatibility does not establish retrieval quality.
