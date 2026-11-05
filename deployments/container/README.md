# Container build maintenance

The repository-root [Containerfile](../../Containerfile) is the canonical product build. This directory's [Dockerfile](Dockerfile) is a byte-identical alternate filename for tools that need it. Build either file with the repository root as its context, not this directory. Edit the canonical file and update the copy in the same change; `make container-sync-check` rejects drift.

The root `.containerignore` and `.dockerignore` share a default-deny context allowlist. Keep them identical and admit new paths only when a build consumes them. Configuration, credentials, local output, Git metadata and documentation dependencies stay outside the context. The separate [functional fixture Containerfile](../../test/functional/Containerfile) uses the same context but produces only a test server, not a product artifact.

## Build boundaries

- The digest-pinned Go builder runs on the build machine's platform and cross-compiles both executables for the selected target. Module acquisition can use the network; compilation cannot. Cache mounts speed up builds but do not make the whole build offline.
- Build one ARM64 image with `make container-build CONTAINER_PLATFORM=linux/arm64 IMAGE=open-agentic-catalog:local-arm64` from the repository root. The native compiler and copy-only scratch stage need no target emulation. `container-verify` also executes the built binaries, so its engine must be able to run the selected architecture; use `container-inspect` to validate a cross-built image without executing it. See [architecture and release-tag behavior](../../documentation/src/content/docs/operations/deployment.mdx#building-for-amd64-and-arm64).
- The `runtime` stage copies the two static, stripped executables and readable `/licenses/` files collected in the native build stage by `licenses.sh`. Their public TLS roots are embedded. Do not copy a shell, development configuration, private trust or credentials into the image.
- The default command is `catalogd api`. Launchers select `worker` separately or override the entrypoint with `/usr/local/bin/catalogctl` for operator commands. The `all` role is only for memory-backed local development.
- The numeric non-root default does not make the filesystem read-only or drop engine-default capabilities. Launchers must supply those restrictions, appropriate runtime inputs and any bounded writable mounts. `EXPOSE` declares ports; it neither publishes them nor starts a listener. Health checks belong to the launcher and use the appropriate role endpoint, not a shell inside the image.

See the maintained [deployment guide](../../documentation/src/content/docs/operations/deployment.mdx#oci-image) for build variables, runtime inputs, architecture limits and operator responsibilities. The [Dockerfile reference](https://docs.docker.com/reference/dockerfile/) defines the underlying instruction and platform-argument semantics.

## Verify changes

Run `make container-check` for synchronization and lint, then `make container-verify` with an explicit local image tag, version and commit. Verification builds the image, checks metadata and copied executable contents, and runs both version commands without network access on a read-only, capability-free container. It exercises the configured `catalogd` entrypoint and rejects failed commands even if they printed the expected metadata. The target leaves the tagged image available locally; it removes its inspection container and temporary files. Nothing is pushed.

Run the relevant repository, Compose and chart gates described in [CONTRIBUTING.md](../../CONTRIBUTING.md). Use `container-inspect`, `licenses` and `sbom` against an existing local image; see the [build/release guide](../../documentation/src/content/docs/contributing/build-and-release.md) for inputs, outputs and chart packaging. Build separate architecture tags in the selected local engine; no image archive, local registry server or combined multi-platform image store is required. Standard image-transfer tools can be used separately when needed. A successful cross-build does not prove native execution on another architecture.
