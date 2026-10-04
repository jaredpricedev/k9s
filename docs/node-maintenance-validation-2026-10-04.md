<!-- Modified for k9+; see NOTICE. -->
# Node maintenance validation — 2026-10-04

The #58 increment uses native Go fixture checks. It does not claim live-cluster
drain integration, a human operator study or measured application recovery.
No Node cordon, drain or Pod mutation was performed against a live cluster.

The domain suite covers affected workloads, grace/local-data/placement evidence,
Secret/environment exclusions, exact Node UID ownership, denied and continued
pages, collection limits, invalid and duplicate Pod identities, wrong-namespace
PDBs, nil/empty/malformed selectors, multiple matching budgets, stale generation,
readiness and unhealthy-Pod policy. A local HTTP server verifies that actual
client-go Node and Pod requests stop when the captured context is canceled.
Fresh all-zero PDB counts remain valid evidence; invalid counts/policy, ambiguous
matches and unknown protected Pod readiness remain uncertainty in the overview.

Native operation fixtures exercise new/recreated/wrong-node/duplicate Pod scope before cordon,
replacement Node identity after accepted cordon, API-denied eviction, native PDB
429 rejection with cancellation, and a partial drain with one accepted eviction
and one denied eviction. Receipts keep accepted cordon/evictions distinct from
observed original Pod removal. The PDB fixture uses API responses; it does not
replace a real admission controller integration test.

SimulationScreen checks inspect rendered cells at 120×34, 80×24, 60×24 and the
40×12 task viewport; a smaller viewport displays the size notice. The tests
exercise keyboard text-entry isolation, tab/search/scroll retention, failed
refresh retention, identity/destination rejection, read-only guards, option-form
cancellation without writes/receipts, and separate post-start recovery evidence.
Marked Node identity remains the target when the cursor moves. Literal evidence
is escaped once, and terminal control sequences are removed from identity chrome.
The next maintenance action requires an observation after the preceding outcome.
All labels and state meanings are present in text without relying on color.

Reproduce with Go 1.25.8:

```sh
go test -ldflags=-w -p 1 ./internal/view ./internal/maintenance \
  -run '^Test(Maintenance|Guarded(Drain|Cordon|Eviction|NativeDelete)|Drain)' -count=1
go test -race -ldflags=-w -p 1 ./internal/view ./internal/maintenance \
  -run '^Test(Maintenance|Guarded(Drain|Cordon|Eviction|NativeDelete)|Drain)' -count=1
```

Hosted CI and integration with the shared responsive modal host are separate
delivery gates. The collector/domain does not invoke commands, controller
reconciliation, Secret reads or automatic recovery mutations.
