# OTLP capture decoder

This test-only command reads a bounded fixture capture from standard input, calls [the shared decoder](../otlp.go), and writes only approved span identity and causality fields as JSON. Compose and Minikube tracing checks use these fields without putting raw telemetry into diagnostics.

The command rejects invalid or privacy-violating captures with a nonzero exit status. Run `go test ./test/tracing/...` for the shared decoder checks; see the [tracing test guide](../README.md) for actual export scenarios.
