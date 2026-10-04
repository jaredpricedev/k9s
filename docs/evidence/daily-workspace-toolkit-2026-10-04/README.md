<!-- Modified for k9+; see NOTICE. -->

# Daily toolkit workspace evidence

Actual PTY cells from the binary and disposable API fixtures described in
[the validation record](../../daily-workspace-validation-2026-10-04.md). No
prototype renderer, live cluster or human usability study was used. PNGs render
the captured terminal cells; matching text files preserve their contents.

- [Daily queue](daily-queue.png) · [cells](daily-queue.txt)
- [Denied Coverage](daily-coverage-denied.png) · [cells](daily-coverage-denied.txt)
- [80×24 scope and inventory](daily-scope-header-80x24.png) · [cells](daily-scope-header-80x24.txt)
- [80×24 investigation](investigation-native-80x24.png) · [cells](investigation-native-80x24.txt)
- [Workspace manifest](workspace-manifest.json) and [request journal](request-journal.json)
- [Investigation manifest](investigation-manifest.json), [skin manifest](skins-manifest.json) and [resource journeys](resource-journeys.json)
- [Complete Go suite](go-tests.txt) and [view race checks](view-race.txt)

The manifests identify the immutable binary SHA-256 and assertions. The
workspace manifest lists all 21 captures; this directory retains a small
selection. CI uploads the complete terminal evidence as an artifact.
