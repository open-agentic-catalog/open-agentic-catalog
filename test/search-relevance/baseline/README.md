# Lexical baseline bridge

This Go command calls the real Control and publication lexical scorers. It accepts a bounded fixture JSON input on stdin and emits score matrices on stdout. The parent relevance runner supplies only reviewed metadata; it owns scope selection, tie order, semantic comparison and reports. Do not copy the scoring algorithms into Python or replace the baseline with another keyword algorithm.

Run `go test ./test/search-relevance/baseline` for its service-free tests. Use the parent Make targets for an evaluation.
