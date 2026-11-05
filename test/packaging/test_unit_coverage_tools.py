"""Coverage command metadata must match the maintained tool lock."""
import unittest

import coverage_tools


class CoverageToolsTests(unittest.TestCase):
    def test_command_compiler_checksum_and_dependency_identity(self):
        package = "github.com/axw/gocov/gocov"
        modules = {"github.com/axw/gocov": ("v1.2.1", "h1:fixture"), "golang.org/x/tools": ("v0.50.0", "h1:tools")}
        metadata = "tool: go1.27.0\n\tpath\t" + package + "\n\tmod\tgithub.com/axw/gocov\tv1.2.1\th1:fixture\n\tdep\tgolang.org/x/tools\tv0.50.0\th1:tools\n"
        coverage_tools.validate(metadata, package, modules, "go1.27.0")
        for invalid in ["", "\n" + metadata, metadata.replace("go1.27.0", "go1.22.2"), metadata.replace(package, "other/command"),
                        metadata.replace("h1:fixture", "h1:changed"), metadata.replace("v0.50.0", "v0.13.0"),
                        metadata.replace("\tmod\t", "\tignored\t"), metadata + "\t=>\tlocal\t(devel)\n",
                        metadata + "\tdep\tgolang.org/x/tools\tv0.50.0\th1:tools\n", metadata.replace("\th1:tools", "")]:
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                coverage_tools.validate(invalid, package, modules, "go1.27.0")
