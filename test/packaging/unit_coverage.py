"""Validate and summarize one Go application unit profile before publication."""

import argparse
from collections import defaultdict
from fractions import Fraction
from pathlib import Path
import re
import sys


def parse_profile(profile):
    """Validate atomic rows and count each source block once."""
    rows = profile.splitlines()
    if not rows or rows[0] != "mode: atomic":
        raise ValueError("expected an atomic Go coverage profile")
    blocks = {}
    for row in rows[1:]:
        match = re.fullmatch(r"(\S+\.go):(\d+\.\d+,\d+\.\d+) (\d+) (\d+)", row)
        if not match:
            raise ValueError("malformed coverage row")
        filename, position, statements, count = match.groups()
        key = (filename, position)
        statements, covered = int(statements), int(count) > 0
        if key in blocks:
            previous, hit = blocks[key]
            if previous != statements:
                raise ValueError("inconsistent statement count: " + filename + ":" + position)
            covered = covered or hit
        blocks[key] = (statements, covered)
    return blocks


def summarize(profile, packages, zero_statements=()):
    """Count each block once, retaining coverage from any contributing package."""
    expected = set(packages.splitlines())
    if not expected or "" in expected:
        raise ValueError("application package selection is empty or malformed")
    blocks = parse_profile(profile)
    files = defaultdict(lambda: [0, 0])
    totals = {package: [0, 0] for package in expected}
    for (filename, _), (statements, covered) in blocks.items():
        package = filename.rsplit("/", 1)[0]
        if package not in expected:
            raise ValueError("unexpected coverage package: " + package)
        for counts in (files[filename], totals[package]):
            counts[0] += statements if covered else 0
            counts[1] += statements
    exempt = set(zero_statements)
    for package, (_, total) in totals.items():
        if package in exempt:
            if total:
                raise ValueError("zero-statement package gained executable code: " + package)
        elif not total:
            raise ValueError("missing executable package coverage: " + package)
    if not sum(counts[1] for counts in totals.values()):
        raise ValueError("application coverage has no executable statements")
    return dict(files), totals


def parse_minimum(minimum, setting):
    """Accept plain decimal percentages, with no rounding during enforcement."""
    if not re.fullmatch(r"[0-9]+(?:\.[0-9]+)?", minimum) or Fraction(minimum) > 100:
        raise ValueError(setting + " must be a number from 0 through 100")
    return Fraction(minimum)


def check_minimum(covered, total, minimum, setting, label):
    threshold = parse_minimum(minimum, setting)
    if total <= 0:
        raise ValueError(label + " has no executable statements")
    actual = Fraction(100 * covered, total)
    if actual < threshold:
        raise ValueError(f"{label}: {covered}/{total} statements ({float(actual):.2f}%) is below the {minimum}% minimum")
    return f"{label}: {covered}/{total} statements ({float(actual):.2f}%), minimum {minimum}%"


def check_critical(packages, required, minimum, setting, label):
    parse_minimum(minimum, setting)
    if not required or any(not package for package in required):
        raise ValueError("critical package selection is empty or malformed")
    lines = []
    for package in sorted(set(required)):
        if package not in packages:
            raise ValueError("critical package is absent from the profile: " + package)
        lines.append(check_minimum(*packages[package], minimum, setting, label + " " + package))
    return lines


def report(files, packages, minimum, critical_packages, critical_minimum):
    covered = sum(value[0] for value in packages.values())
    total = sum(value[1] for value in packages.values())
    lines = [check_minimum(covered, total, minimum, "UNIT_COVERAGE_MIN", "Application unit coverage")]
    lines.extend(check_critical(packages, critical_packages, critical_minimum,
                                "UNIT_CRITICAL_PACKAGE_COVERAGE_MIN", "Critical unit package"))
    for label, groups in (("Packages", packages), ("Files", files)):
        lines.append("\n" + label + " (covered/total statements):")
        for name, (hits, count) in sorted(groups.items()):
            percent = f"{100 * hits / count:.2f}%" if count else "n/a (no executable statements)"
            lines.append(f"{hits:6}/{count:<6} {percent:>8}  {name}")
    return "\n".join(lines)


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("packages", type=Path)
    parser.add_argument("--minimum", required=True)
    parser.add_argument("--critical-packages", required=True)
    parser.add_argument("--critical-minimum", required=True)
    parser.add_argument("--zero-statements", action="append", default=[])
    options = parser.parse_args(arguments)
    try:
        files, packages = summarize(options.profile.read_text(), options.packages.read_text(), options.zero_statements)
        print(report(files, packages, options.minimum, options.critical_packages.split(), options.critical_minimum))
    except (OSError, ValueError) as error:
        print("Unit coverage failed: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
