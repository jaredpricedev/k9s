# Inline Flux reconcile smoke and demo

`inline-reconcile.mp4` is a continuous replay of a real K9s terminal session at its
original pace. The table contains 10,000 synthetic HelmReleases. The title above
the terminal explicitly identifies the local fake API and scripted controller.
The screenshots contain only cells emitted by K9s.

This verifies the native inline workflow and UI responsiveness. It does **not**
measure real Flux controller reconciliation speed, cluster performance, or monitor
paint latency. There is no Flux controller, Helm process, external cluster, or
Flux CLI in this fixture. The fake API's PATCH response and controller updates
are released by explicit harness signals; their elapsed times are not benchmarks.

## Reproduce

Build the repository, then run from its root:

```sh
go build -o /tmp/k9s-flux-inline .
python scripts/flux-inline-demo.py --binary /tmp/k9s-flux-inline
```

Dependencies: Python 3, pyte, Pillow, ffmpeg, and DejaVu Sans/Mono fonts. Capture
uses the PTY reader and replay renderer from `scripts/perf-demo.py`, which imports
the Kubernetes discovery fixture from `scripts/capture-demo.py`.

For smoke assertions and screenshots without video encoding:

```sh
python scripts/flux-inline-demo.py --binary /tmp/k9s-flux-inline --no-video
```

Re-render the captured terminal without rerunning the application:

```sh
python scripts/flux-inline-demo.py --render-only assets/flux-inline/results.json
```

The harness selects only a disposable loopback kubeconfig. K9s configuration,
logs, all XDG directories, and `KUBECACHEDIR` are isolated under a temporary
directory. The native `helmreleases default` command opens the fixture directly.

## Assertions

- The initial native table contains 10,000 HelmReleases.
- An installed legacy `Shift-R`, `override: true` plugin cannot replace native
  reconciliation. A second plugin's visible `Sentinel probe` shortcut proves the
  same configuration file loaded. A sentinel `flux` executable records any
  unexpected invocation; it must never run.
- `Shift-R` shows native confirmation. Enter on its default Cancel button causes
  no object GET or PATCH.
- Confirmation sends exactly one selected-object GET and one merge PATCH with
  UID/resourceVersion preconditions and `reconcile.fluxcd.io/requestedAt`.
- The server publishes the accepted annotation through its existing watch while
  deliberately withholding the PATCH response. The selected row becomes
  `Reconciling` even though its previous controller condition was `Ready`.
- The PTY receives and displays filtering and selection movement during this
  HTTP wait. Repeated reconcile input before and after filtering is suppressed.
- After the PATCH response completes, the controller is held separately.
  Filtering and movement continue, and the cached pending request prevents a
  second reconcile submission.
- Scripted controller condition updates and matching
  `status.lastHandledReconcileAt` acknowledgments yield `Ready` and `Failed` for
  the two selected resources. Restoring the full table shows both outcomes.
- The entire run has one HelmRelease watch, at most one list, exactly two GETs,
  and exactly two PATCHes. Initial-events streaming can supply all 10,000 objects
  without a separate list. There are no per-row polling requests.

## Artifacts

- `results.json`: binary SHA-256, fixture, assertions, interaction timestamps,
  controller timing, screenshot timestamps, and video provenance.
- `api-requests.json`: recorded requests, selected names, PATCH bodies, and
  content types. Request times and controller times share the API origin;
  interaction and screenshot times use the PTY capture origin.
- `session.cast.gz`: gzip-compressed asciicast v2 input/output events.
- `01`–`08` PNG/TXT files: loaded table, native confirmation, accepted pending
  request, navigation/filtering during both delays, and terminal outcomes.
- `k9s.log`: application log from this isolated run.
- `reconciling.png`: a copy of the full table with the accepted request pending.
- `inline-reconcile.mp4`: continuous terminal replay with an explanatory title.

Rasterization and encoding run only after capture. The terminal display is never
redrawn from an invented UI model. Filters use Enter and an explicit Home key to
normalize the table's scroll position before subsequent row movement checks.
