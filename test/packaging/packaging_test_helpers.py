
"""Local-only inspection, chart contents and failure cleanup regressions."""
import hashlib

IDENTIFIER = "sha256:" + "a" * 64

def settings():
    return dict(Id=IDENTIFIER, Os="linux", Architecture="amd64", Config=dict(
        User="65532:65532", WorkingDir="/var/lib/open-agentic-catalog", Entrypoint=["/usr/local/bin/catalogd"],
        Cmd=["api"], StopSignal="SIGTERM", ExposedPorts={"8080/tcp": {}, "9090/tcp": {}}, Labels={
            "org.opencontainers.image.version": "dev", "io.open-agentic-catalog.source-state": "unknown", "org.opencontainers.image.revision": "unknown",
            "org.opencontainers.image.title": "Open Agentic Catalog",
            "org.opencontainers.image.source": "https://github.com/open-agentic-catalog/open-agentic-catalog"}))

def license_tree(directory):
    """Create runtime-readable image inputs inside the caller's private fixture."""
    for name in ["project/LICENSE", "go/LICENSE", "dependencies/example/LICENSE", "upstream/example/LICENSE",
                 "samples/artwork/README.md", "samples/first-party/catalog-discovery/LICENSE",
                 "samples/packages/catalog-discovery-tools/NOTICE.txt", "samples/packages/frontend-toolkit/NOTICE.txt",
                 "samples/packages/web-testing-toolkit/NOTICE.txt", "samples/upstream/skills/brand-guidelines/LICENSE.txt",
                 "samples/upstream/skills/frontend-design/LICENSE.txt", "samples/upstream/skills/webapp-testing/LICENSE.txt"]:
        path = directory / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("example")
    (directory / "upstream/checksums.txt").write_text(hashlib.sha256(b"example").hexdigest() + "  upstream/example/LICENSE\n")
    for path in [directory, *directory.rglob("*")]:
        path.chmod(0o755 if path.is_dir() else 0o644)
