"""Validate build-bound UI evidence extracted from the selected local image."""
import hashlib
import base64
import json
from pathlib import PurePosixPath
import re
from urllib.parse import quote


def regular_tree(root):
    """Every copied asset must be readable by the non-root runtime identity."""
    for path in [root, *root.rglob("*")]:
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError("unexpected UI asset file type")
        required = 0o005 if path.is_dir() else 0o004
        if path.stat().st_mode & required != required:
            raise ValueError("UI assets are not runtime-readable")


def relative(value):
    if not isinstance(value, str) or not value or "\\" in value:
        raise ValueError("invalid UI evidence path")
    path = PurePosixPath(value)
    if path.is_absolute() or str(path) != value or any(p in (".", "..") for p in path.parts):
        raise ValueError("invalid UI evidence path")
    return value


def inventory(directory):
    """Never compare an older image with the current checkout's lockfile."""
    assets, licenses = directory / "assets", directory / "licenses/frontend"
    regular_tree(assets)
    value = json.loads((licenses / "inventory.json").read_bytes())
    if value.get("schemaVersion") != 1 or value.get("scope") != "locked-production-dependencies" or not re.fullmatch(r"[0-9a-f]{64}", value.get("lockSha256", "")):
        raise ValueError("invalid UI build inventory")
    paths = set()
    for asset in value["assets"]:
        path = relative(asset["path"])
        if path in paths or hashlib.sha256((assets / path).read_bytes()).hexdigest() != asset["sha256"]:
            raise ValueError("UI asset inventory mismatch")
        paths.add(path)
    actual = {str(p.relative_to(assets)) for p in assets.rglob("*") if p.is_file() and not p.relative_to(assets).parts[0] == "docs"}
    if paths != actual or "index.html" not in paths or not any(p.endswith(".js") for p in paths):
        raise ValueError("UI asset inventory incomplete")
    packages = set()
    for package in value["packages"]:
        path = relative(package["path"])
        if not path.startswith("node_modules/") or path in packages or package["name"] != path.split("node_modules/")[-1]:
            raise ValueError("invalid UI package identity")
        if not all(isinstance(package.get(k), str) and package[k] for k in ("version", "license")) or not package.get("notices"):
            raise ValueError("missing UI package version/license")
        for notice in package["notices"]:
            if not relative(notice).startswith(path + "/") or not (licenses / notice).read_bytes():
                raise ValueError("missing UI package notice")
        packages.add(path)
    if not packages:
        raise ValueError("missing UI production packages")
    return value


def documentation_inventory(directory):
    """Verify the complete static manual against evidence from the same image."""
    assets = directory / "assets/docs"
    licenses = directory / "licenses/documentation"
    regular_tree(assets)
    name = "documentation-bundle.json"
    manifest = (assets / name).read_bytes()
    if not 0 < len(manifest) <= 2 << 20 or manifest != (licenses / name).read_bytes():
        raise ValueError("missing or mismatched manual manifest")
    value = json.loads(manifest)
    if value.get("schemaVersion") != 1 or value.get("base") != "/docs/" or not re.fullmatch(r"[a-f0-9]{64}", value.get("lockSha256", "")):
        raise ValueError("invalid manual manifest")
    for key in ("scriptHashes", "styleHashes"):
        hashes = value[key]
        if not isinstance(hashes, list) or len(hashes) > 64 or len(set(hashes)) != len(hashes):
            raise ValueError("invalid manual policy hashes")
        for policy_hash in hashes:
            if not re.fullmatch(r"sha256-[A-Za-z0-9+/]{43}=", policy_hash):
                raise ValueError("invalid manual policy hash")
            decoded = base64.b64decode(policy_hash[7:], validate=True)
            if len(decoded) != 32 or base64.b64encode(decoded).decode() != policy_hash[7:]:
                raise ValueError("invalid manual policy hash")
    paths, total = set(), 0
    for asset in value["files"]:
        path = relative(asset["path"])
        if path == name or path in paths or len(path) > 512 or not re.fullmatch(r"[A-Za-z0-9_./-]+", path) or any(p.startswith(".") for p in PurePosixPath(path).parts):
            raise ValueError("invalid manual inventory path")
        if (assets / path).stat().st_size > 32 << 20:
            raise ValueError("manual asset exceeds bounds")
        data = (assets / path).read_bytes()
        total += len(data)
        if len(data) != asset["size"] or len(data) > 32 << 20 or hashlib.sha256(data).hexdigest() != asset["sha256"]:
            raise ValueError("manual asset mismatch")
        if re.search(r"\.(?:node|so|dll|dylib|exe)$", path):
            raise ValueError("build tool shipped in manual")
        paths.add(path)
    actual = {str(p.relative_to(assets)) for p in assets.rglob("*") if p.is_file()}
    if not 1 <= len(paths) <= 10000 or total > 256 << 20 or actual != paths | {name} or "index.html" not in paths:
        raise ValueError("incomplete manual inventory")
    for notice in ("dependency-inventory.json", "third-party-notices.txt", "browser-inventory.json"):
        if not (assets / "generated" / notice).read_bytes() or (assets / "generated" / notice).read_bytes() != (licenses / notice).read_bytes():
            raise ValueError("manual notices mismatch")
    suppliers = json.loads((licenses / "browser-inventory.json").read_bytes())
    # The inclusive lock evidence preserves SPDX strings and legacy arrays.
    # Browser suppliers use reviewed strings; build-only arrays remain valid.
    build_packages = {(p["name"], p["version"], json.dumps(p["license"])) for p in json.loads((licenses / "dependency-inventory.json").read_bytes())}
    if suppliers.get("schemaVersion") != 1 or suppliers.get("scope") != "reviewed-browser-asset-suppliers" or suppliers.get("lockSha256") != value["lockSha256"]:
        raise ValueError("invalid manual supplier evidence")
    identities = set()
    notice_text = (licenses / "third-party-notices.txt").read_text()
    for package in suppliers["packages"]:
        identity = (package["name"], package["version"])
        if identity in identities or not all(isinstance(package.get(k), str) and package[k] for k in ("name", "version", "license")) or re.search(r"MPL|LGPL", package["license"]):
            raise ValueError("invalid manual browser supplier")
        identities.add(identity)
        if (*identity, json.dumps(package["license"])) not in build_packages:
            raise ValueError("manual supplier absent from build inventory")
        notices = package.get("notices")
        if not isinstance(notices, list) or not notices or len(notices) != len(set(notices)):
            raise ValueError("missing manual supplier notices")
        for notice in notices:
            relative(notice)
            if f"Notice: {identity[0]}@{identity[1]} / {notice}\n" not in notice_text:
                raise ValueError("manual supplier notice absent from bundle")
    if not identities:
        raise ValueError("missing manual browser suppliers")
    return suppliers


def scan_inventory(directory):
    """Scanner sees frontend suppliers and manual suppliers, never build-only tools."""
    frontend = inventory(directory)
    manual = documentation_inventory(directory)
    identities = {(p["name"], p["version"]): p for p in frontend["packages"] + manual["packages"]}
    return {"packages": list(identities.values())}


def supplement(document, directory, identifier, platform):
    """Merge shipped frontend evidence into the scanner's host Go inventory."""
    if document.get("spdxVersion") != "SPDX-2.3" or not document.get("SPDXID"):
        raise ValueError("missing SPDX package inventory")
    names = {p.get("name") for p in document.get("packages", [])}
    if not {"usr/local/bin/catalog-ui", "github.com/open-agentic-catalog/open-agentic-catalog"}.issubset(names):
        raise ValueError("SBOM did not identify UI host Go dependencies")
    value = inventory(directory)
    manual = documentation_inventory(directory)
    for package, scope in [(p, "frontend") for p in value["packages"]] + [(p, "documentation") for p in manual["packages"]]:
        package_id = "SPDXRef-UI-" + hashlib.sha256((scope + ":" + package["name"] + "@" + package["version"]).encode()).hexdigest()
        document["packages"].append({"SPDXID": package_id, "name": package["name"], "versionInfo": package["version"],
            "downloadLocation": "NOASSERTION", "filesAnalyzed": False, "licenseConcluded": "NOASSERTION",
            "licenseDeclared": package["license"], "copyrightText": "NOASSERTION",
            "externalRefs": [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl",
                              "referenceLocator": "pkg:npm/" + quote(package["name"], safe="/") + "@" + quote(package["version"], safe="")}],
            "sourceInfo": ("Conservative locked production closure; /licenses/frontend/inventory.json; lock sha256:" + value["lockSha256"] if scope == "frontend" else "Reviewed browser asset suppliers; /licenses/documentation/browser-inventory.json; lock sha256:" + manual["lockSha256"])})
        document.setdefault("relationships", []).append({"spdxElementId": document["SPDXID"], "relationshipType": "DESCRIBES", "relatedSpdxElement": package_id})
    document["comment"] = "Local UI image " + identifier + "; platform " + platform + ". Frontend and manual hashes verified. Conservative browser supplier evidence is not an exact tree-shaken module inventory; documentation build tools are excluded."
