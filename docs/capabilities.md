# Capability diagnostics

Use `:diagnostics` to choose an explicit check, or run one directly:

- `:diagnostics metrics`: probes one timestamped node sample from metrics.k8s.io.
- `:diagnostics flux`: checks list access to Kustomizations, HelmReleases and GitRepositories in the active namespace.
- `:diagnostics cert-manager`: checks Certificates and Issuers in the active namespace.
- `:diagnostics resource`: checks the selected canonical API object and its UID when known.
- `:diagnostics hubble`: checks the current context's configured Relay with one Observer.ServerStatus request.

Ordinary resource browsing does not invoke these checks. Each API list asks for at most one object. Reads use captured client handles, an eight-second deadline, cancellation on exit, and a context/view generation guard before displaying results. `r` requests a new observation without the application's discovery cache. Returning to a retained report labels it **STALE** until it is checked again. A resource UID that was unknown remains explicitly unknown.

The report distinguishes **API absent**, **permission denied**, **not configured**, **unavailable**, **TLS failure**, **connection failure**, **stale**, and **available**, and prints the exact source, observation time, unmet prerequisite and recovery action. Available means the named prerequisite was readable; it is not a verdict about workload health, traffic health, or complete coverage. An advertised metrics API returning HTTP 503 is unavailable. An empty sample is unavailable. A timestamped measured zero is valid. Observations older than two minutes are stale.

## Hubble setup

Configure Relay for the chosen kube context in its context `config.yaml`:

```yaml
k9s:
  hubble:
    address: relay.example.com:4245
    serverName: relay.example.com
    caFile: /absolute/path/relay-ca.pem
    # Set both when Relay requires mutual TLS:
    # certFile: /absolute/path/client.pem
    # keyFile: /absolute/path/client-key.pem
```

The default transport verifies the server certificate and hostname. Invalid CA PEM, an incomplete certificate/key pair, wrong hostname/trust, denied ServerStatus and a refused/timed-out connection produce distinct states and recovery instructions. Run `:diagnostics hubble`, fix the reported prerequisite, and press `r` after context settings have reloaded (reopen diagnostics after changing credentials). After a fresh successful check, Enter opens Relay status. Select a pod or supported workload to scope subsequent flow observation. Coverage and loss must still be assessed in Hubble.

`plaintext: true` is an explicit opt-in for an endpoint that was set up for plaintext, such as a separately managed local tunnel. It cannot be combined with TLS fields. Diagnostics states that TLS identity was not verified; it never claims a verified TLS connection for plaintext.

Port-forward supervision is a separate increment. It must require an explicit context, namespace, Relay target and local port; own the process and stop it on exit; detect port collisions and process failure; show the active destination and connection loss. Diagnostics does not launch a port-forward, change network policy, or infer traffic health.

## Persistent destination and metrics

The top strip always shows the current context, namespace, connection state and plain `[RO]` or `[RW]` mode, including compact/no-icons modes and prompts. Transient messages use the footer. Long names truncate by terminal-cell width; F2 opens the complete destination, including cluster/user and metric source/observation time.

Choose `k9s.ui.headerMode: auto`, `compact`, or `full`. Ctrl+E sets a manual preference for the current app session. To emphasize production deliberately, configure exact context names:

```yaml
k9s:
  ui:
    productionContexts:
      - production-us-east
```

CPU and memory headers and Pulse use explicit sample availability. Missing/denied/unconfigured samples render N/A rather than zero; last-good values retained after failure are labelled stale. Stale and missing samples do not enter trends or health thresholds. Memory changes compare previous memory with current memory independently of CPU. F2 reveals header metric provenance; `m` in Pulse shows the source and observation time for its current namespace series.

Pod, container and node rows display `n/a` for unavailable CPU or memory usage while keeping requests and limits from the resource spec. A pod aggregate requires that dimension for every app container and restartable init container; partial coverage is unknown. Timestamped measured zero remains `0`. Namespace Pulse shows CPU and memory usage totals, without a capacity percentage or health threshold; cluster Pulse retains utilization against node allocatable capacity.
