# Native Hubble first increment

Goal: investigate workload connectivity inside k9plus using Relay gRPC.

Approved scope: per-context endpoint/TLS, status and node coverage, reported loss,
peers -> bidirectional conversation -> safe detail, pod jumps, structured server
filters, local search, stable frozen inspection. No policy mutation, L7 enabling,
DNS analysis, captures, policy proposals, or pin mode.

Architecture: internal/hubble owns the official Cilium protobuf client, safe
normalized events, bounded store and filter compilation. internal/view owns all
presentation state on the draw goroutine. Ingestion never updates widgets.
Separate bounded history and live requests label provenance; any handoff or
reconnect gap is disclosed. Counts are observed events, never connections.

Tasks:
- [x] Add tests for exact bidirectional scope, malformed filters, safe L7 projection,
  non-pod identity, bounded eviction and immutable snapshots; implement model.
- [x] Add context config/TLS validation and native Observer client. Verify against
  Cilium v1.18.1 observer/flow proto and official TLS docs. Test with real gRPC server.
- [x] Add :cilium and Shift-H from pod/workload resource views. Freeze before
  movement/Enter; preserve peer/event identity and scroll; explicit resume.
- [x] Exercise allowed/drop/external/no-L7/disconnect/high-volume behavior in a
  separate private iximiuz Cilium lab, preserving logging lab.
- [x] Build, run focused tests, review diff, document limitations and open PR.

Key audit: Shift-H unused by resource/app/table/extender bindings. Existing logs
use s for follow, navigation freezes, Enter expands, Esc backtracks. Network views
use s freeze/resume, / filter, Enter drilldown, 1/2 source/destination pod, r retry.

Safety: retain only allowlisted L7 fields (type/protocol/status/latency); no HTTP
URLs, headers, DNS query payloads, summaries, or arbitrary protobuf extensions.
Names and terminal text are sanitized. No exports in this increment.
