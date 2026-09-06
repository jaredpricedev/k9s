#!/usr/bin/env python3
# Modified for k9+; see NOTICE.
# Copyright 2026 the k9+ contributors.
# SPDX-License-Identifier: Apache-2.0
"""Collect intact license and attribution files for release Go dependencies.

Uses the package dependency closure, not the unused go.mod module graph.
The result is evidence for review, not a legal opinion or SPDX classifier.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_TARGETS = (
    "linux/amd64", "linux/arm64", "linux/arm", "linux/ppc64le", "linux/s390x",
    "freebsd/amd64", "freebsd/arm64", "darwin/amd64", "darwin/arm64",
    "windows/amd64", "windows/arm64",
)
NOTICE_NAME = re.compile(r"^(licen[cs]e|copying|notice|copyright|authors)(?:$|[._-])", re.I)
LICENSE_NAME = re.compile(r"^(licen[cs]e|copying)(?:$|[._-])", re.I)


def run_go(*args, env=None):
    return subprocess.check_output(["go", *args], cwd=ROOT, env=env, text=True)


def json_stream(text):
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            return
        item, offset = decoder.raw_decode(text, offset)
        yield item


def notice_files(root):
    # Keep full upstream files, including nested notices for bundled components.
    for directory, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in {".git", ".hg", ".svn"})
        for name in sorted(files):
            path = Path(directory) / name
            if NOTICE_NAME.match(name) or "LICENSES" in path.relative_to(root).parts[:-1]:
                if path.is_symlink():
                    raise RuntimeError(f"refusing symlinked notice: {path}")
                yield path


def review_signals(content):
    # These deliberately conservative signals are not license identification.
    text = content.decode("utf-8", errors="replace").lower()
    signals = []
    for phrase, label in (
        ("gnu general public license", "GPL text/reference"),
        ("gnu lesser general public license", "LGPL text/reference"),
        ("gnu affero general public license", "AGPL text/reference"),
        ("mozilla public license", "MPL text/reference"),
        ("eclipse public license", "EPL text/reference"),
        ("not be used to develop a rar", "unRAR restriction"),
    ):
        if phrase in text:
            signals.append(label)
    return signals


def collect(output, targets):
    modules = {}
    for target in targets:
        goos, goarch = target.split("/")
        env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch, GOARM="7")
        print(f"Collecting dependencies for {target}", file=sys.stderr)
        for package in json_stream(run_go("list", "-mod=readonly", "-deps", "-json", ".", env=env)):
            module = package.get("Module")
            if not module or module.get("Main"):
                continue
            selected = module.get("Replace", module)
            if not selected.get("Version") or not selected.get("Dir"):
                raise RuntimeError(f"unversioned or unavailable dependency: {module['Path']}")
            key = (module["Path"], module.get("Version", ""), selected["Path"], selected["Version"])
            entry = modules.setdefault(key, {"module": module["Path"], "version": module.get("Version", ""),
                "source_module": selected["Path"], "source_version": selected["Version"],
                "root": selected["Dir"], "packages": set(), "targets": set()})
            entry["packages"].add(package["ImportPath"])
            entry["targets"].add(target)
    if not modules:
        raise RuntimeError("no third-party dependencies found; refusing an empty notice bundle")

    output = output.resolve()
    if output == ROOT or ROOT.is_relative_to(output):
        raise RuntimeError("output must not replace the source tree or its ancestors")
    output.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix=".third-party-licenses-", dir=output.parent))
    try:
        manifest = {"schema_version": 1, "generator": "scripts/collect-licenses.py",
                    "go_version": run_go("version").strip(), "cgo_enabled": False,
                    "targets": targets, "dependencies": []}
        for key in sorted(modules):
            entry = modules[key]
            root = Path(entry.pop("root"))
            files = list(notice_files(root))
            if not any(LICENSE_NAME.match(p.name) for p in files):
                raise RuntimeError(f"no LICENSE/COPYING evidence for {entry['module']}@{entry['version']}")
            destination = Path("modules") / (entry["source_module"] + "@" + entry["source_version"])
            records = []
            for path in files:
                relative = path.relative_to(root)
                dest = stage / destination / relative
                dest.parent.mkdir(parents=True, exist_ok=True)
                data = path.read_bytes()
                dest.write_bytes(data)
                records.append({"source": relative.as_posix(), "file": (destination / relative).as_posix(),
                                "sha256": hashlib.sha256(data).hexdigest(), "review_signals": review_signals(data)})
            entry["packages"] = sorted(entry["packages"])
            entry["targets"] = sorted(entry["targets"])
            entry["files"] = records
            # MPL executable distributions must make covered source available.
            # Preserve the exact original module archive alongside its notices.
            if any("MPL text/reference" in record["review_signals"] for record in records):
                downloaded = json.loads(run_go("mod", "download", "-json",
                                              entry["source_module"] + "@" + entry["source_version"]))
                archive = Path(downloaded.get("Zip", ""))
                if not archive.is_file():
                    raise RuntimeError(f"MPL source archive unavailable: {entry['module']}")
                relative_archive = Path("sources") / (entry["source_module"] + "@" + entry["source_version"] + ".zip")
                destination_archive = stage / relative_archive
                destination_archive.parent.mkdir(parents=True, exist_ok=True)
                data = archive.read_bytes()
                destination_archive.write_bytes(data)
                entry["source_archive"] = {"file": relative_archive.as_posix(),
                    "sha256": hashlib.sha256(data).hexdigest(), "reason": "MPL source availability"}
            manifest["dependencies"].append(entry)

        # The standard library/runtime is linked too; Go includes BSD notices
        # and third-party attributions under its source tree.
        goroot = Path(run_go("env", "GOROOT").strip())
        go_files = [goroot / "LICENSE", goroot / "PATENTS"]
        go_files.extend(notice_files(goroot / "src"))
        if not (goroot / "LICENSE").is_file():
            raise RuntimeError("Go toolchain LICENSE is missing")
        manifest["go_toolchain_files"] = []
        for path in sorted(set(go_files)):
            if not path.is_file():
                continue
            relative = path.relative_to(goroot)
            dest = stage / "go-toolchain" / relative
            dest.parent.mkdir(parents=True, exist_ok=True)
            data = path.read_bytes()
            dest.write_bytes(data)
            manifest["go_toolchain_files"].append({"source": relative.as_posix(),
                "file": (Path("go-toolchain") / relative).as_posix(), "sha256": hashlib.sha256(data).hexdigest()})
        # Notice evidence can include files named license.go/copyright.go.
        # A nested module keeps parent `go test ./...` and linters from treating
        # this generated distribution material as application source packages.
        (stage / "go.mod").write_text(
            "// Generated notice bundle; not a buildable application module.\n"
            "// SPDX-License-Identifier: Apache-2.0\n"
            "module example.invalid/k9plus-third-party-notices\n\n"
            "go 1.18\n")
        (stage / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        (stage / "README.txt").write_text(
            "k9+ third-party license and attribution evidence\n\n"
            "Generated from go list -deps for the target matrix in manifest.json, with CGO disabled.\n"
            "Files are copied in full from the exact selected Go modules and Go toolchain.\n"
            "Nested notices can describe bundled files beyond the compiled package closure.\n"
            "Review signals are text matches, not legal classifications or clearance.\n"
            "Source for modules with MPL notices is provided in the sources/ ZIP archives.\n"
            "That source remains governed by its included licenses, including MPL-2.0.\n"
            "The spdx/tools-golang Apache-2.0 OR GPL option is used under Apache-2.0 here.\n"
            "This bundle does not change any dependency license. See docs/licensing.md.\n")
        # A failed collection leaves the previous successful output intact and
        # exits nonzero; release hooks must propagate this failure.
        if output.exists():
            previous_manifest = output / "manifest.json"
            try:
                previous = json.loads(previous_manifest.read_text())
            except (OSError, ValueError):
                previous = {}
            if previous.get("generator") != "scripts/collect-licenses.py":
                raise RuntimeError(f"output is not a generated license bundle: {output}")
            shutil.rmtree(output)
        stage.rename(output)
        print(f"Saved {len(modules)} dependency modules to {output}")
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT / "THIRD_PARTY_LICENSES")
    parser.add_argument("--target", action="append", help="GOOS/GOARCH; repeat for a target union")
    args = parser.parse_args()
    targets = sorted(set(args.target or DEFAULT_TARGETS))
    if any(not re.fullmatch(r"[a-z0-9]+/[a-z0-9]+", target) for target in targets):
        parser.error("targets must have GOOS/GOARCH form")
    try:
        collect(args.output, targets)
    except (OSError, RuntimeError, subprocess.CalledProcessError, ValueError) as error:
        print(f"License collection failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
