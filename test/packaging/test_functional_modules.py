"""Keep ignored Go experiments out of module checks without hiding current source."""

from pathlib import Path
import subprocess
import tempfile
import unittest

import modules


class ModuleSourceTests(unittest.TestCase):
    def test_current_source_includes_edits_and_new_files_but_not_ignored_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "source"
            target = Path(directory) / "stage"
            root.mkdir()
            subprocess.run(["git", "init", "--quiet", str(root)], check=True)
            (root / ".gitignore").write_text("output/\nnode_modules/\n.env\n")
            (root / "go.mod").write_text("module example.com/catalog\n")
            (root / "main.go").write_text("package catalog\n")
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            (root / "main.go").write_text("package updated\n")
            (root / "new.go").write_text("package updated\n")
            (root / "output").mkdir()
            (root / "output/experiment.go").write_text('import "unavailable.invalid/module"\n')
            (root / ".env").write_text("private")
            modules.stage(root, target)
            self.assertEqual((target / "main.go").read_text(), "package updated\n")
            self.assertTrue((target / "new.go").exists())
            self.assertFalse((target / "output").exists())
            self.assertFalse((target / ".env").exists())
            (root / "link.go").symlink_to(root / "main.go")
            with self.assertRaisesRegex(ValueError, "symlinked source"):
                modules.stage(root, target)


if __name__ == "__main__":
    unittest.main()
