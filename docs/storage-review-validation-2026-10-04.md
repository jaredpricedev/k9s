<!-- Modified for k9+; see NOTICE. -->
# Storage review validation — 2026-10-04

The #57 increment was checked with native Go tests and the actual application in
a PTY against disposable loopback API fixtures. These are fixture checks; no
live-cluster, CSI-driver integration or human operator study is claimed.

Domain tests cover independent denied CSI/claim/Pod sources, bounded and continued
pages, selected replacement UIDs, namespace-safe consumer joins, PV claim UID
matching, WaitForFirstConsumer, attachRequired=false and absent attachments.
Historical mount events on running Pods retain an explicit event label and time.
Capacity stays separate from usage; requests, status capacity, conditions and
allocated resize statuses remain separate. A real HTTP collection checks all
eight source query bounds and cancels a stalled CSI request without discarding
completed readable sources. Collector tests pass with the race detector.

Expansion tests verify strict increase, unknown/deleting/non-Bound claims,
missing class/provisioner/expansion permission, UID/version requirements and
verified PV binding. Native worker tests cover authorization denial, changed
PVC/class UID or version, changed PV UID or claim binding, equal/lower quantities,
size-only conditional patches and separate outcomes for two PVC targets. A
disposable HTTP server actually replaces its PVC after GET and rejects the
PATCH's original UID/version tests; the worker does not retry. The accepted
variant persists the larger request while capacity and filesystem work remain
unchanged. These tests exercise transport/worker contracts, not Kubernetes API
server or CSI implementation conformance.

SimulationScreen checks inspect emitted native cells at 120, 80, 60 and 40
columns, the task-size notice and resize recovery. They retain tab, search,
scroll, original evidence after refresh failure and destination ownership.
Native form input checks the current typed quantity, two-stage preview, read-only
submission rejection, stopped dialogs and superseded generations. Shared
zero-field confirmations retain three guidance rows where space permits, with
PgUp/PgDn for the full preview and persistent context/action controls.

`scripts/probe-storage.py` drives the built application with actual keyboard
input and captures emitted cells, requests and the conditional patch. It covers
all five diagnosis tabs, denied CSI alongside readable topology/attachments,
request versus status capacity, pending filesystem work, one next check per
stage, expansion input/confirmation and read-only submission rejection. The
resize sequence is 120×34 → 80×24 → 60×24 → 40×16 → 40×12 notice → 60×24 with
the CSI tab retained. A second native run explicitly submits one PVC increase,
opens its guarded-operation ACCEPTED receipt, refreshes still-pending capacity/
conditions and verifies failed-refresh retention and Back to the selected PVC.
No icons are enabled, and states/actions remain in text without color dependence.

The request journal checks three fixed eight-source collections with 101 objects
requested per source, no Secret API reads and exactly one explicitly confirmed
resource write. The patch tests UID and resourceVersion atomically and replaces
only `/spec/resources/requests/storage`. Authorization-review POSTs are separate
from resource mutations. Both runs quit normally through the keyboard.

Reproduce with Go 1.25.8 and Python with Pillow/pyte:

```sh
go test -ldflags=-w -p 1 ./internal/storage -count=1
go test -ldflags=-w -p 1 ./internal/view -run 'TestStorage' -count=1
go test -race -ldflags=-w -p 1 ./internal/storage -count=1
go build -buildvcs=false -ldflags=-w -p 1 -o /tmp/k9plus-storage .
python scripts/probe-storage.py --repo "$PWD" --binary /tmp/k9plus-storage \
  --output /tmp/k9plus-storage-captures
```

The native capture directory contains PNGs, matching terminal text,
`api-requests.json`, `conditional-patch.json` and `manifest.json` with source
revision, clean/dirty state, binary SHA-256 and capture sizes/times. Development
artifacts are retained at
`/workspace/artifacts/k9plus-storage-review-2026-10-04/pty`.

Local pinned golangci-lint 2.6.2 new-change checks for the affected storage/view
packages pass. Integration and hosted checks are separate delivery gates. Optional
volume-usage providers, complete historical events, actual CSI runtime capability
checks and controller/filesystem completion remain explicit limits.
