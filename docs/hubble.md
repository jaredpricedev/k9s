# Native Cilium / Hubble (first increment)

Select a pod, Deployment, DaemonSet, StatefulSet, ReplicaSet or Job and press
**Shift-H**. The view resolves the workload's current pods and shows its observed
peers, including pods, world, external IPs, reported FQDNs and kube-apiserver.
Press **Enter** for both conversation directions, then **Enter** for safe event
detail. **1** / **2** jump to source / destination pods; **Esc** backtracks.
`:cilium` (or `:hubble`) opens Relay status and node coverage.

## Context configuration

Run `k9plus info` to find Context Configs, then edit the active context's
`config.yaml` (`clusters/<cluster>/<context>/config.yaml`). Add:

```yaml
k9s:
  hubble:
    address: 127.0.0.1:4245
    plaintext: true
    clusterName: kubernetes
```

`clusterName` is Cilium's cluster name, **not** the kubeconfig context name.
When set, the client limits scope to that reported cluster. Pod jumps require
it to match the reported endpoint cluster, avoiding same-name ClusterMesh jumps.
With no clusterName, Relay scope can include same-named pods in other clusters;
the UI includes reported cluster names in pod labels and refuses unverified jumps.

The example uses a separately established local port-forward:

```sh
kubectl -n kube-system port-forward svc/hubble-relay 4245:80
```

The first increment does not manage port-forwards. No `hubble` executable is needed.
For a TLS endpoint, omit `plaintext` (TLS is the default) and use:

```yaml
k9s:
  hubble:
    address: relay.example.com:443
    clusterName: production
    serverName: relay.example.com
    caFile: /absolute/path/ca.crt
    certFile: /absolute/path/client.crt
    keyFile: /absolute/path/client.key
```

System roots are used when caFile is omitted. certFile/keyFile are optional
unless the Relay requires mutual TLS; provide both together. Server identity
verification is mandatory. Plaintext cannot be mixed with TLS settings. These
settings describe the client-to-Relay connection; Relay-to-agent TLS is separate.
Certificate changes take effect on reconnect. Restart k9plus after config edits.

## Inspection and filtering

- Navigation, mouse inspection, Enter, help and filtering freeze the **last
  displayed dataset**, preserving selection even if the ingestion ring rotates.
- **s** toggles freeze/resume. **r** reconnects, refreshes workload pod membership,
  and replaces the retained dataset. Leaving for a pod stops observation;
  back returns the frozen view, and resume reconnects.
- **/** plain text searches locally. Structured AND expressions use `field=value`:
  `verdict=dropped protocol=tcp port=443 ip=10.0.0.0/8`. Supported fields are
  `verdict`, `protocol`, `port` (either endpoint), and `ip` (either endpoint).
  Unsupported fields/operators, duplicates and malformed values are errors and
  preserve the previous filter. A valid structured change starts a new stream.
- Server filters preserve workload scope and expand symmetric fields into OR
  branches. Exact pod matching locally compensates for Hubble's prefix matching.
  Server rejection is displayed; the client never silently broadens a query.

## Visibility and safety limits

Counts are **observed events**, not requests, connections, packets or sessions.
Several observation points may report the same traffic. Peers and conversation
counts cover only the retained filtered dataset, not durable analytics.

The client requests recent buffered history (number=500) and then live events
in separate requests. Rows label their origin. **The handoff and reconnect can
have gaps**, and clocks/Relay retention limit history. The 10,000-event local
ring is bounded; frozen snapshots are independent of it. Local evictions and
Relay-reported lost events have separate counters. Zero reported loss does not
prove complete visibility. Node coverage is Relay-reported, not a separate audit
of all Kubernetes nodes; unavailable or unimplemented coverage is labeled unknown.

L7 payloads, URLs, headers, raw summaries and arbitrary extensions are discarded
before retention. Only safe protocol/status metadata is shown. No L7 record means
**visibility unknown**. DNS success is not interpreted as policy authorization.
Policy names appear only when the flow explicitly reports attribution; a policy
drop reason alone does not establish which policy caused it. FQDN names are
reported endpoint metadata and can be absent or stale; IP identity stays visible.

The client does not enable L7, change policies, or expose raw export/recording.
Saved captures, policy links/proposals, DNS analysis, drop triage and pin mode
remain later increments. Workload membership is resolved on connection, not
continuously watched. More than 1,000 selected pods is rejected explicitly.

## API sources and verification

Uses official Cilium v1.18.1 protobuf Go bindings for Observer.GetFlows,
Observer.ServerStatus and Observer.GetNodes. The first increment was exercised
against Cilium/Hubble Relay 1.20.1; a broader version compatibility matrix has
not been tested.

- [Observer protobuf](https://github.com/cilium/cilium/blob/v1.18.1/api/v1/observer/observer.proto)
- [Flow and filter protobuf](https://github.com/cilium/cilium/blob/v1.18.1/api/v1/flow/flow.proto)
- [Hubble observability](https://docs.cilium.io/en/stable/observability/hubble/)
- [TLS configuration](https://docs.cilium.io/en/stable/observability/hubble/configuration/tls/)
- [Reproducible lab checks](../scripts/hubble/README.md)
