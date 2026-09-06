<!-- Modified for k9+; see NOTICE. -->

<!-- Copyright 2026 the k9+ contributors. SPDX-License-Identifier: Apache-2.0 -->

# Licensing and attribution

k9+ is an independently maintained fork of [K9s](https://github.com/derailed/k9s).
The upstream baseline used for the fork's modification inventory is commit
`84852e6`. The original Apache License 2.0 text in [LICENSE](../LICENSE), the
upstream [COPYING](../COPYING) attribution, and existing source copyright notices
are retained. New k9+ code and documentation are also licensed under Apache-2.0.

## Redistribution

The [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0), sections
4(a)–4(d), requires a license copy for recipients, prominent change notices on
modified files, retention of applicable upstream source notices, and propagation
of applicable NOTICE attributions if the upstream distribution has a NOTICE.
Modified files carry k9+ change notices; [MODIFICATIONS.md](../MODIFICATIONS.md)
provides the change inventory and summary. Keep these notices when redistributing
this fork, and mark any further modifications you make.

The tracked upstream tree at `84852e6` contains `LICENSE` and `COPYING` and no
`NOTICE` file. This fork adds [NOTICE](../NOTICE) to explain its provenance and
independent maintenance. The notice supplements attribution; it does not alter
the license or replace the per-file modification notices.

## Names and branding

Apache-2.0 section 6 limits its trademark permission to customary identification
of a work's origin and reproduction of NOTICE content. It does not grant general
permission to use upstream product names or logos. k9+ does not claim affiliation
with or endorsement by the K9s maintainers. References to K9s in copyright
headers, historical material, upstream links, compatibility names, and the Go
module path identify provenance or technical compatibility.

The choice of `k9+` is not a completed trademark search or legal clearance of that
name. The license and attribution work described here does not establish rights
to a name or logo, nor is it a comprehensive legal review of distribution.

## Third-party dependencies in binary releases

Run `make licenses` (or `python3 scripts/collect-licenses.py`) before packaging.
The collector requires Python 3.9+ and the Go version selected by `go.mod`.
It generates `THIRD_PARTY_LICENSES/` with full license, COPYING, NOTICE,
copyright, and author files copied from the selected module cache, plus Go
runtime/toolchain notices. `manifest.json` records exact module and replacement
versions, imported package paths, target coverage, original file paths, hashes,
and text matches that need review. Modules whose notices mention MPL also
include their complete original Go module source ZIPs under `sources/`; the
manifest identifies each archive and its hash. These unmodified dependency
sources retain their own licenses, including MPL-2.0.

The default target matrix matches the release configuration: Linux amd64,
arm64, armv7, ppc64le and s390x; FreeBSD amd64 and arm64; macOS amd64 and arm64;
and Windows amd64 and arm64. All use `CGO_ENABLED=0`, and ARM uses `GOARM=7`.
The collector enumerates the package dependency closure with `go list -deps .`
for each target, excluding unused module-graph entries and test-only imports.
Module-level and nested notice files are retained conservatively, so a copied
notice may describe a bundled component outside the compiled package closure.
It does not parse individual source-file license headers or prove which code
survives linker elimination.

A custom build can narrow the bundle with repeated options, for example:

```sh
python3 scripts/collect-licenses.py --target linux/amd64 --target linux/arm64
```

Keep the script's default matrix aligned with `.goreleaser.yml` when adding
platforms. Builds with different tags, CGO, external native libraries, or a
modified dependency set require a matching notice and license review; the
current collector covers the configured Go releases. Container base-image
software is additional to this Go dependency inventory and retains its own
license and source obligations.

The command exits unsuccessfully if package resolution fails, a dependency is
unversioned/unavailable, or a selected dependency has no LICENSE/COPYING evidence.
Release hooks must stop on this failure. A successful bundle is evidence for
review, not a finding that every dependency has a compatible license. In
particular, references to GPL/LGPL/AGPL, MPL, EPL, and unRAR restrictions are
flagged for examination. A text match can be an alternative license or an
exception, and absence of a match is not a license classification. Review new or
changed dependencies before publishing. Do not treat the top-level Apache
license as relicensing third-party software.

Archives include `LICENSE`, `COPYING`, `NOTICE`, `MODIFICATIONS.md`, and the
generated `THIRD_PARTY_LICENSES` tree. Linux packages and container images keep
these materials under `/usr/share/doc/k9plus/`. Source distributions retain
upstream notices and this guide; dependencies fetched separately retain their
own licenses.

## Dependency review for this revision

The initial default-matrix scan selected 377 modules and retained 579 module
notice files. No selected module lacked LICENSE/COPYING evidence. This is not a
formal SPDX classification of every source file. The identified MPL modules
are listed below; their original source archives accompany binary releases to
provide recipients the covered source described by
[MPL-2.0 section 3.2](https://www.mozilla.org/en-US/MPL/2.0/).

| Module | Version | Observed license |
| --- | --- | --- |
| `github.com/anchore/go-version` | `v1.2.2-0.20210903204242-51efa5b487c4` | MPL-2.0 |
| `github.com/cyphar/filepath-securejoin` | `v0.6.1` | BSD-3-Clause and MPL-2.0; per-file coverage |
| `github.com/hashicorp/aws-sdk-go-base/v2` | `v2.0.0-beta.72` | MPL-2.0 |
| `github.com/hashicorp/errwrap` | `v1.1.0` | MPL-2.0 |
| `github.com/hashicorp/go-cleanhttp` | `v0.5.2` | MPL-2.0 |
| `github.com/hashicorp/go-getter` | `v1.8.6` | MPL-2.0 |
| `github.com/hashicorp/go-multierror` | `v1.1.1` | MPL-2.0 |
| `github.com/hashicorp/go-version` | `v1.8.0` | MPL-2.0 |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | MPL-2.0 |
| `github.com/hashicorp/hcl/v2` | `v2.24.0` | MPL-2.0 |

`github.com/spdx/tools-golang v0.5.7` explicitly offers its code under
Apache-2.0 **or** GPL-2.0-or-later in `LICENSE.code`; k9+ uses the Apache-2.0
option and retains the complete file. GPL/LGPL mentions inside the MPL license
are references to secondary licenses, not evidence that this fork elected a
GPL license. Additional GPL/LGPL matches came from conservatively copied Syft
and modernc libc test fixtures, outside the listed imported test packages;
those matches alone do not establish a linked GPL requirement.
`github.com/therootcompany/xz v1.0.1` uses CC0-1.0, as stated in its LICENSE.

The automated scan found no additional GPL-only root license text and no
AGPL/EPL/unRAR text matches. These text searches do not settle per-file,
embedded-data, patent, trademark, or transitive native-library questions. The
exact files and review signals in each generated manifest take precedence over
these counts when dependencies or targets change. If covered dependency source
is modified in a later release, provide that modified source instead of relying
on an original upstream module archive.
