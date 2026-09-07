# Log workbench

The log workbench turns container logs into selectable entries with runtime time,
source, severity and message columns. Open logs from a workload to keep watching
its replacement pods during a rollout. Opening a pod follows that pod's container
generations; it does not silently switch to a different workload.

## Keyboard guide

Open `:deployments`, select a workload and press `l`. Start with `mixed-logs` in
namespace `logging-demo`. Press `?` inside logs for the complete in-place help.

| What to try | Keys |
|---|---|
| Structured query or raw regex | `/`, expression, Enter |
| Expand fields and grouped original lines | Enter; Escape returns |
| Freeze displayed entries / resume live newest | Arrows, j/k or PageUp; `s` toggles |
| Raw message columns, still redacted | `o` |
| Collapse repeats / group stack traces | `b` / `u` |
| Top patterns and matching entries | `p`, select, Enter |
| Isolate / exclude / reset sources | `i` / `x` / `z` |
| Compare two sources in lanes | `v`, select lane A, Enter, select lane B, Enter |
| Refresh a secondary view | `g` |
| Time buckets / largest spike | `h`, select, Enter / `J` |
| Stack a noise exclusion | `/`, `-healthz`, Enter; repeat for another rule |
| Inspect rules and recording status | `n` |
| Save / clear local noise profile | `N` / `X` |
| Start / stop safe recording | `R` |
| Page older / latest recorded entries | `[` / `]` |
| Search recording using current filter | `D` |
| Open a prior recording read-only | `O`, select, Enter |
| Export the whole retained recording | `E` |
| Export currently visible rows | Ctrl-S |
| Copy entry / mark a range boundary | `c` / `m` |
| Clear the live display | Shift-C |
| Reconnect from a server timestamp | `G`, RFC3339 timestamp, Enter |
| Toggle display redaction | `d` |
| Explicitly start a raw recording | Ctrl-R, after any active recording closes |
| Wrap expanded text / fullscreen | `w` / `f` |

Shift-C clears the live render ring and returns to the live entry table. The
active query/noise rules, monotonic entry IDs, source privacy tracking and disk
recording remain intact. Pending groups are flushed to recording before the
clear; subsequent identical lines begin a fresh collapse count. Recorded history
can still be opened independently.

Recording starts only when requested. `R` closes an active recording and drains
its queue in the background; press it again after closure to start another.
Pressing R while a session is still being prepared cancels that start.
Ctrl-R is a separate raw-recording choice. Copy and export remain safe even when
display redaction is off or the source recording is raw.

Pressing `s`, or navigating with arrows, j/k, PageUp/PageDown, Home or End,
freezes the displayed entry snapshot at the existing live buffer bound. Rows,
selection, filters, detail and safe copy/export continue to use that snapshot
even if newer capture evicts those entries from the live engine. Press `s` to
resume the live view at its newest matching entry and release the snapshot.
Capture and recording continue while the display is frozen; the status identifies
the view as live or frozen.

Details, patterns, lanes, history and help keep collection running. They hold their
selection while new data arrives; `g` refreshes secondary views explicitly.
Copy, mark and visible export operate on entry tables or details. Selectors and
comparison views explain when an entry action is unavailable. Historical time
jumps stay within the selected recording.

Expanded entry details put the original observed log lines first. Parsed fields
follow as indented, syntax-colored JSON, then compact muted k9+ provenance and a
separate controls footer. Warning and error badges use the active skin's status
colors; labels and separators keep every section understandable without color.
Log text and parsed keys or values are sanitized, redacted when safe display is
enabled, and escaped before dynamic terminal styling is applied.

Informational markers remain visible through content/severity filters, and source
isolation/exclusion also applies to markers. A source gutter identifies the pod,
container and restart generation.

The existing `0`–`6` time windows, `a` all-containers control where available, `t`
runtime timestamp display and `L` horizontal column lock remain available.

## Queries and noise

JSON objects and logfmt are parsed per entry. Mixed or malformed lines remain raw
text; a format sample never permanently disables parsing. Application timestamps
remain separate from Kubernetes runtime timestamps.

Examples for the filter prompt:

```text
level>=error
http.status>=500
level>=warn AND http.status>=500
trace_id=abc
msg="cache miss"
timeout|connection refused
```

Comparisons are typed. Quote numeric-looking strings when you mean a string.
Missing fields do not match, including `!=`. Severity order is trace, debug, info,
warn, error, fatal. Plain expressions use regular expressions against the whole
raw entry, including grouped continuation lines. Invalid expressions keep the last
working filter.

Noise rules are literal negative substrings, stacked independently of the query.
Local profiles are scoped by context, namespace and an application label, with a
workload kind/name fallback. The default label preference is
`app.kubernetes.io/name`, then `app`. Teams can provide a JSON array on their
workload or pod template:

```yaml
metadata:
  annotations:
    k9plus.io/log-noise: '["healthz", "kube-probe"]'
```

The annotation is data only. Saving or clearing a local profile never patches a
Kubernetes resource.

## Patterns, grouping and time

Exact consecutive duplicates can fold into one row with a repeat count. Expanded
rows preserve the representative original text and report the first and last
times; recording retains the individual entries before collapse. Pattern analysis
normalizes identifiers and addresses while preserving diagnostic numbers such as
HTTP status codes. A 200 response must remain distinguishable from a 500 response.

Multiline groups are isolated by pod UID, container and restart generation and are
bounded in size and time. The rate histogram counts observed physical lines,
including folded repetitions and continuation lines. Its scope is retained data,
not an assertion about every line ever emitted by the application.

Cross-pod ordering is approximate. Runtime timestamps come from node clocks, and a
bounded reorder window cannot correct arbitrary clock skew. Kubernetes Events can
be delayed, aggregated or unavailable. Marker provenance distinguishes Events,
pod status and client-observed source changes.

## Recording and sharing

Default recordings and copied/exported snippets are redacted. Turning off display
redaction does not enable raw recording. Raw recording is a separate, explicit
session choice. Recordings use private files and bounded retention; recording
errors leave the live stream usable.

Redaction recognizes JWTs, bearer tokens, private-key blocks and AWS-shaped access
key IDs. This is a heuristic and cannot recognize every application-specific
credential. Terminal controls are neutralized and log text is escaped at the UI
boundary.

Kubernetes provides no server-side content filtering or seek. A server timestamp
jump opens a new request with `sinceTime`; paging and searching an existing
recording operate locally. `previous` covers at most one available terminated
container generation. Node rotation caps available history, and the API serves
the latest available log file. Recording can only preserve lines this client
actually observed.

## Reproducible playground

```bash
kubectl apply -f docs/examples/logging-demo.yaml
k9plus -n logging-demo -c deployments
```

Select `mixed-logs` for JSON/logfmt, nested HTTP fields, repeats, stack traces and
synthetic credential shapes; `noisy-sidecar` for raw noise and Python traces; or
`controlled-crash` for deliberate container exits and restart markers. The latter
intentionally enters backoff. All examples contain synthetic data.

Each `mixed-logs` replica also has a noisy `sidecar` container. Its default log
container is `app`; use the all-containers control to compare the two sources.

While watching workload logs in another terminal:

```bash
bash docs/examples/logging-demo-actions.sh replace
bash docs/examples/logging-demo-actions.sh rollout
bash docs/examples/logging-demo-actions.sh event
bash docs/examples/logging-demo-actions.sh burst
```

`scale 4` adds a replica; `scale 3` restores the default. These actions affect only
the `logging-demo` namespace. Existing Flux and certificate demonstrations are
independent.

## Bounds and recording durability

The following defaults bound the entry pipeline. The live status reports when a
bound causes eviction, truncation, forced ordering or lost records.

| Resource | Default bound |
|---|---:|
| Entries retained for live rendering | 5,000 (`logger.buffer`) |
| Active Kubernetes log streams | 32 |
| One physical log line | 64 KiB |
| One multiline group | 64 lines / 64 KiB |
| Multiline idle flush | 500 ms |
| Timestamp reorder window | 250 ms / 2,048 pending entries |
| Rate histogram | 60 one-second buckets |
| Pending recording queue | 4,096 entries / 8 MiB serialized data |
| One recording session | 128 MiB including metadata / 24 hours |
| One recording segment | 8 MiB |
| Recording index | 100,000 entries |

Recording writes use bounded batches with up to 100 ms of buffering. A clean
close drains the writer; abrupt client termination can lose buffered entries or
an unfinished batch. An uncertain recording checkpoint resumes conservatively,
which can mask additional text. Client queue loss, disk retention and Kubernetes
history limits are separate from one another; a recording is not a complete
cluster log archive.

Recording status and `n` inspection separate `admission-drop` (entries rejected
before entering the writer queue) from `disk-evict` (segments removed by the
recording size/retention bounds). These counters remain visible after recording
closes. A writer error explicitly marks accepted batch/queued records as possibly
unwritten or of uncertain durability; the admission count and last processed ID
are not a durable-loss count. Copy/export metadata includes current-session loss
counters and failure context when available. For independently reopened historical
sessions whose counters are unavailable, metadata says so rather than reporting
zero loss.

These settings belong under the existing `k9s.logger` section of the k9+
configuration file. Zero or omitted recording settings use the defaults shown.

```yaml
k9s:
  logger:
    buffer: 5000
    recordingSessions: 8
    recordingRetentionHours: 24
    recordingMaxMiB: 128
    noiseAppLabels:
      - app.kubernetes.io/name
      - app
```

Recordings are stored in `log-recordings/` beneath the k9+ configuration directory;
local noise profiles use `log-noise.json` there. The default eight-session limit
bounds retained recording data to roughly 1 GiB in total. Active sessions are
protected from retention cleanup; reaching the session cap reports an error
rather than interrupting live logs. Reducing a cap takes effect when a new session
is opened.
