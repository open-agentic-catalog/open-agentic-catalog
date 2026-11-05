"""Collect process counters and reconcile fresh Go application profiles."""

import argparse
from collections import defaultdict
from pathlib import Path
import subprocess
import sys

from unit_coverage import check_critical, check_minimum, parse_profile


def encode(blocks):
    return "mode: atomic\n" + "".join(
        f"{filename}:{position} {statements} {int(hit)}\n"
        for (filename, position), (statements, hit) in sorted(blocks.items())
    )


def merge(base, additions, allow_harness=False):
    """Keep the base inventory, rejecting changed or partial source files."""
    result = dict(base)
    if not base:
        raise ValueError("empty base profile")
    files = defaultdict(set)
    for key in base:
        files[key[0]].add(key)
    for extra in additions:
        if not extra:
            raise ValueError("empty contributing profile")
        selected = {}
        for key, value in extra.items():
            if key[0] not in files:
                if allow_harness and any(part in key[0] for part in ("/test/", "/examples/", "/internal/integration/")):
                    continue
                raise ValueError("unexpected coverage file: " + key[0])
            selected[key] = value
        for filename in {key[0] for key in selected}:
            if {key for key in selected if key[0] == filename} != files[filename]:
                raise ValueError("incompatible source blocks: " + filename)
        for key, (statements, hit) in selected.items():
            if base[key][0] != statements:
                raise ValueError("incompatible statement weight: " + key[0])
            result[key] = (statements, result[key][1] or hit)
    return result


def totals(blocks):
    files = defaultdict(lambda: [0, 0])
    for (filename, _), (statements, hit) in blocks.items():
        files[filename][0] += statements if hit else 0
        files[filename][1] += statements
    covered = sum(value[0] for value in files.values())
    total = sum(value[1] for value in files.values())
    if total == 0:
        raise ValueError("profile has no executable statements")
    return files, covered, total


def check_profile(blocks, minimum, scope, critical_packages=None, critical_minimum=None):
    """Enforce each existing local/core/database scope without changing its inventory."""
    setting, label = {
        "local": ("COVERAGE_MIN", "Broad local coverage"),
        "core": ("CORE_COVERAGE_MIN", "Local core coverage"),
        "postgres": ("POSTGRES_COVERAGE_MIN", "PostgreSQL package coverage"),
    }[scope]
    files, hits, count = totals(blocks)
    lines = [check_minimum(hits, count, minimum, setting, label)]
    if scope == "core":
        packages = defaultdict(lambda: [0, 0])
        for file, counts in files.items():
            package = file.rsplit("/", 1)[0]
            packages[package][0] += counts[0]
            packages[package][1] += counts[1]
        lines.extend(check_critical(packages, (critical_packages or "").split(), critical_minimum or "",
                                    "CRITICAL_PACKAGE_COVERAGE_MIN", "Critical local package"))
    elif critical_packages is not None or critical_minimum is not None:
        raise ValueError("critical local checks require the core profile")
    return "\n".join(lines)


def reconcile(unit, local, postgres, processes, minimum):
    if not unit.keys() <= local.keys():
        raise ValueError("local profile omits application source")
    database = {key for key in unit if "/internal/platform/database/" in key[0]}
    if postgres.keys() != database:
        raise ValueError("PostgreSQL profile inventory differs from database source")
    expected_process_files = {key[0] for key in unit if any(key[0].endswith("/" + file) for file in (
        "cmd/catalogctl/main.go", "cmd/catalogd/main.go", "internal/platform/runtime/runtime.go"))}
    if not expected_process_files <= {key[0] for key in processes}:
        raise ValueError("process profile omits a required application file")
    combined = merge(unit, [local, postgres, processes], allow_harness=True)
    files, _, _ = totals(combined)
    lines = []
    for name, profile in (("Unit application", unit), ("Local application", {k: v for k, v in local.items() if k in unit}),
                          ("PostgreSQL package", postgres), ("PostgreSQL processes", processes), ("Combined application", combined)):
        _, hits, count = totals(profile)
        lines.append(f"{name}: {hits}/{count} statements ({100 * hits / count:.2f}%)")
    lines.append(f"\nApplication files: {len(files)}; per-file minimum: 80%")
    for file, (hits, count) in sorted(files.items(), key=lambda item: (item[1][0] / item[1][1] if item[1][1] else 1, item[0])):
        if count:
            lines.append(f"{100 * hits / count:6.2f}% {hits:5}/{count:<5} {file}")
    low = [file for file, (hits, count) in files.items() if 100 * hits < 80 * count]
    if low:
        raise ValueError("application files below 80%: " + ", ".join(sorted(low)))
    _, hits, count = totals(combined)
    lines.append(check_minimum(hits, count, minimum, "FULL_COVERAGE_MIN", "Combined application floor"))
    return combined, "\n".join(lines) + "\n"


def collect(directory, output, go):
    directories = sorted({path.parent for path in directory.rglob("covmeta.*")})
    if not directories:
        raise ValueError("no process coverage metadata")
    # Missing counters must not silently turn a failed process into zero coverage.
    if any(not list(path.glob("covcounters.*")) for path in directories):
        raise ValueError("process coverage metadata has no counters")
    subprocess.run([go, "tool", "covdata", "textfmt", "-i=" + ",".join(str(path) for path in directories),
                    "-o=" + str(output)], check=True, timeout=60)
    if not parse_profile(output.read_text()):
        raise ValueError("empty process coverage profile")


def snapshot():
    # Reuse the functional supervisor's bounded, path-sensitive source hashing.
    sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "functional"))
    from runner import digest_files
    paths = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                           check=True, capture_output=True, timeout=10).stdout.decode()
    return digest_files(set(paths.rstrip("\0").split("\0")))


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest="action", required=True)
    actions.add_parser("snapshot")
    collection = actions.add_parser("collect")
    collection.add_argument("directory", type=Path)
    collection.add_argument("output", type=Path)
    collection.add_argument("--go", default="go")
    merging = actions.add_parser("merge")
    merging.add_argument("base", type=Path)
    merging.add_argument("extra", type=Path)
    merging.add_argument("output", type=Path)
    checking = actions.add_parser("check")
    checking.add_argument("profile", type=Path)
    checking.add_argument("--minimum", required=True)
    checking.add_argument("--scope", choices=("local", "core", "postgres"), required=True)
    checking.add_argument("--critical-packages")
    checking.add_argument("--critical-minimum")
    full = actions.add_parser("full")
    for name in ("unit", "local", "postgres", "processes", "output", "report"):
        full.add_argument(name, type=Path)
    full.add_argument("--minimum", required=True)
    options = parser.parse_args(arguments)
    try:
        if options.action == "snapshot":
            print(snapshot())
        elif options.action == "collect":
            collect(options.directory, options.output, options.go)
        elif options.action == "merge":
            result = merge(parse_profile(options.base.read_text()), [parse_profile(options.extra.read_text())])
            options.output.write_text(encode(result))
        elif options.action == "check":
            print(check_profile(parse_profile(options.profile.read_text()), options.minimum, options.scope,
                                options.critical_packages, options.critical_minimum))
        else:
            result, report = reconcile(*(parse_profile(getattr(options, name).read_text()) for name in ("unit", "local", "postgres", "processes")),
                                       options.minimum)
            options.output.write_text(encode(result))
            options.report.write_text(report)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print("Application coverage failed: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
