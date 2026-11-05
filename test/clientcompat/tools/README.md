# Go client generator dependencies

This isolated module pins the OpenAPI Go generator and its transitive dependencies. It is separate from both the service module and the [consumer test module](../go/README.md), so generator requirements do not enter the shipped backend.

Use `make client-go-tools-install` to install the locked tooling and `make client-go-check` to verify fresh, reproducible generation. Update the locks deliberately according to the [harness guide](../README.md); do not place generated clients in this directory.
