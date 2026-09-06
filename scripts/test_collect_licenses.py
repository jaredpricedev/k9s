# Modified for k9+; see NOTICE.
# Copyright 2026 the k9+ contributors.
# SPDX-License-Identifier: Apache-2.0
"""Contract tests for release notice collection using isolated Go evidence."""
import importlib.util
import json
import os
import shutil
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("collect_licenses", Path(__file__).with_name("collect-licenses.py"))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


class CollectorTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.module = self.root / "module"
        self.module.mkdir()
        self.goroot = self.root / "go"
        (self.goroot / "src").mkdir(parents=True)
        (self.goroot / "LICENSE").write_text("Go license\n")
        self.output = self.root / "output"
        self.zip = self.root / "module.zip"
        self.zip.write_bytes(b"complete original source archive fixture")

    def go(self, *args, env=None):
        if args[0] == "list":
            return json.dumps({"ImportPath": "example.org/module/pkg", "Module": {
                "Path": "example.org/module", "Version": "v1.0.0", "Dir": str(self.module)}})
        if args == ("version",):
            return "go version fixture"
        if args == ("env", "GOROOT"):
            return str(self.goroot)
        if args[:2] == ("mod", "download"):
            return json.dumps({"Zip": str(self.zip)})
        raise AssertionError(args)

    def test_missing_license_fails_without_replacing_previous_bundle(self):
        self.output.mkdir()
        (self.output / "sentinel").write_text("keep")
        with patch.object(collector, "run_go", self.go):
            with self.assertRaisesRegex(RuntimeError, "no LICENSE/COPYING"):
                collector.collect(self.output, ["linux/amd64"])
        self.assertEqual((self.output / "sentinel").read_text(), "keep")

    def test_refuses_to_replace_an_unrelated_output_directory(self):
        (self.module / "LICENSE").write_text("permission fixture")
        self.output.mkdir()
        (self.output / "unrelated.txt").write_text("keep")
        with patch.object(collector, "run_go", self.go):
            with self.assertRaisesRegex(RuntimeError, "not a generated license bundle"):
                collector.collect(self.output, ["linux/amd64"])
        self.assertEqual((self.output / "unrelated.txt").read_text(), "keep")

    @unittest.skipUnless(shutil.which("go"), "Go is needed to verify package traversal")
    def test_generated_bundle_is_excluded_from_go_package_discovery(self):
        workspace = self.root / "workspace"
        workspace.mkdir()
        (workspace / "go.mod").write_text("module example.org/application\n\ngo 1.18\n")
        (workspace / "main.go").write_text("package main\nfunc main() {}\n")
        self.output = workspace / "THIRD_PARTY_LICENSES"
        (self.module / "LICENSE").write_text("permission fixture")
        # Real upstream modules contain Go files named license.go/copyright.go.
        # These are preserved by the conservative notice-file collector.
        (self.module / "license.go").write_text("package licensing\n")
        with patch.object(collector, "run_go", self.go):
            collector.collect(self.output, ["linux/amd64"])
        result = subprocess.run(["go", "list", "./..."], cwd=workspace,
                                env=dict(os.environ, GOTOOLCHAIN="local", GOWORK="off"),
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "example.org/application")

    def test_full_notice_bytes_target_union_and_mpl_source_are_delivered(self):
        original = b"Mozilla Public License, version 2.0\r\nCopyright Example\xff\n"
        (self.module / "LICENSE").write_bytes(original)
        (self.module / "NOTICE").write_text("upstream attribution\n")
        with patch.object(collector, "run_go", self.go):
            collector.collect(self.output, ["linux/amd64", "windows/arm64"])
        manifest = json.loads((self.output / "manifest.json").read_text())
        dep = manifest["dependencies"][0]
        self.assertEqual(dep["targets"], ["linux/amd64", "windows/arm64"])
        license_file = next(f for f in dep["files"] if f["source"] == "LICENSE")
        self.assertEqual((self.output / license_file["file"]).read_bytes(), original)
        self.assertEqual((self.output / dep["source_archive"]["file"]).read_bytes(), self.zip.read_bytes())


if __name__ == "__main__":
    unittest.main()
