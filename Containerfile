# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
# Build from the repository root; keep the compatibility Dockerfile identical.
# The compiler runs natively while TARGETOS/TARGETARCH select the output platform.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build

WORKDIR /src

ENV GOTOOLCHAIN=local

ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE_STATE=unknown
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
# Dependency/tool acquisition is the only build step that needs network access.
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && \
    go mod verify && \
    GOBIN=/tools go install github.com/google/go-licenses@v1.6.0

COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY schemas ./schemas
COPY samples ./samples
COPY LICENSE ./LICENSE
COPY deployments/container/licenses.sh /tools/licenses.sh

# The collector executes natively; GOOS/GOARCH select each target's dependencies.
RUN --network=none \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS='-tags=netgo,osusergo' sh /tools/licenses.sh

RUN --network=none \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=readonly -trimpath -buildvcs=false -tags=netgo,osusergo -ldflags="-s -w -buildid= -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.sourceState=${SOURCE_STATE}" -o /out/ ./cmd/catalogd ./cmd/catalogctl

FROM scratch AS runtime

ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE_STATE=unknown

LABEL org.opencontainers.image.title="Open Agentic Catalog" \
      org.opencontainers.image.description="Governed catalog and discovery service for Agents, MCP Servers, Skills, and Plugins" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      io.open-agentic-catalog.source-state="${SOURCE_STATE}" \
      org.opencontainers.image.source="https://github.com/open-agentic-catalog/open-agentic-catalog"

# Public TLS roots are embedded in both binaries. Configuration, private trust,
# credentials and writable mounts belong to the launcher, not the image.
COPY --from=build --chmod=0555 /out/catalogd /usr/local/bin/catalogd
COPY --from=build --chmod=0555 /out/catalogctl /usr/local/bin/catalogctl
COPY --from=build /out/licenses/ /licenses/

WORKDIR /var/lib/open-agentic-catalog
USER 65532:65532
EXPOSE 8080 9090
# No shell-based health check: launchers select the role's catalogctl probe.
STOPSIGNAL SIGTERM

ENTRYPOINT ["/usr/local/bin/catalogd"]
CMD ["api"]
