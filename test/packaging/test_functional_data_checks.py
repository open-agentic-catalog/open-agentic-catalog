"""Git and command boundaries for read-only data checks use disposable inputs."""

import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

import data_checks


class DataWorkflowTests(unittest.TestCase):
    def test_inventory_uses_worktree_handles_names_and_preserves_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            subprocess.run(['git', 'init', '-q', directory], check=True)
            (root / '.gitignore').write_text('output/\n')
            (root / 'old.json').write_text('{}')
            (root / 'removed.json').write_text('{}')
            (root / 'edited.json').write_text('{}')
            subprocess.run(['git', '-C', directory, 'add', '.'], check=True)
            (root / 'old.json').rename(root / 'renamed.json')
            (root / 'removed.json').unlink()
            (root / 'edited.json').write_text('{"duplicate":1,"duplicate":2}')
            (root / 'space and\nnewline.jsonld').write_text('{}')
            (root / '-options.json').write_text('{}')
            (root / 'output').mkdir()
            (root / 'output/bad.json').write_text('invalid')
            with patch.dict(os.environ, {'GIT_DIR': '/unavailable/git'}):
                names = data_checks.source_files(root, {'.json', '.jsonld'})
            self.assertEqual(set(map(str, names)), {'renamed.json', 'edited.json', 'space and\nnewline.jsonld', '-options.json'})
            with self.assertRaisesRegex(ValueError, 'edited.json'):
                data_checks.check_json(root)
            self.assertEqual((root / 'edited.json').read_text(), '{"duplicate":1,"duplicate":2}')
            (root / 'edited.json').write_text('{}')
            data_checks.check_json(root)
            (root / 'linked.json').symlink_to(root / 'edited.json')
            with self.assertRaisesRegex(ValueError, 'symlinked'):
                data_checks.source_files(root, {'.json'})

    def test_actual_json_make_target_accepts_valid_and_propagates_invalid_data(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            subprocess.run(['git', 'init', '-q', directory], check=True)
            for name in ('Makefile', 'go.mod', 'test/packaging/data_checks.py'):
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(data_checks.ROOT / name, target)
            fixture = root / 'fixture.json'
            for content, valid in (('{"ok":1}', True), ('{"dup":1,"dup":2}', False)):
                fixture.write_text(content)
                result = subprocess.run(
                    ['make', '--no-print-directory', 'json-check', 'CATALOG_ENV_FILE=/dev/null',
                     'PYTHON=' + sys.executable], cwd=root, capture_output=True, text=True,
                    timeout=15, check=False,
                )
                self.assertEqual(result.returncode == 0, valid, result.stdout + result.stderr)
                if not valid:
                    self.assertIn('duplicate JSON object key', result.stderr)
                self.assertEqual(fixture.read_text(), content)

    def test_git_failure_is_not_an_empty_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            # Stop ancestor discovery even when TMPDIR is beneath a checkout.
            (root / '.git').write_text('gitdir: missing-git-directory\n')
            with self.assertRaises(subprocess.CalledProcessError):
                data_checks.source_files(root, {'.json'})

    def test_yaml_invocation_preserves_paths_config_and_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            subprocess.run(['git', 'init', '-q', directory], check=True)
            (root / '.yamllint.yml').write_text('extends: default\n')
            (root / 'space name.yaml').write_text('name: example\n')
            required = ['compose.embeddings.yaml', 'test/embeddings/semantic.compose.yaml',
                        'test/clientcompat/generate.yaml',
                        'deployments/helm/open-agentic-catalog/ci/semantic-values.yaml']
            for name in required:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('name: example\n')
            upstream = root / 'schemas/upstream/example.yaml'
            upstream.parent.mkdir(parents=True)
            upstream.write_text('name: example\n')
            tool = root / 'lint tool'
            log = root / 'arguments'
            tool.write_text('#!/usr/bin/env python3\nimport json, sys\nfrom pathlib import Path\np=Path(__file__).with_name("arguments")\nwith p.open("a") as f: f.write(json.dumps(sys.argv[1:])+"\\n")\n')
            tool.chmod(0o755)
            data_checks.check_yaml(root, str(tool))
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual(len(calls), 2)
            self.assertEqual(calls[0][:3], ['-c', str(root / '.yamllint.yml'), '--'])
            self.assertIn(str(root / 'space name.yaml'), calls[0])
            for name in required:
                self.assertIn(str(root / name), calls[0])
            self.assertEqual(calls[1], ['-d', '{"rules": {"key-duplicates": "enable"}}', '--', str(upstream)])
            tool.write_text('#!/bin/sh\nexit 7\n')
            with self.assertRaises(subprocess.CalledProcessError):
                data_checks.check_yaml(root, str(tool))


if __name__ == '__main__':
    unittest.main()
