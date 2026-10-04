# Reviewed change-set API-server trial — 2026-10-04

The audit used official Kubernetes SIG controller-tools envtest 1.35.0 assets: `https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v1.35.0/envtest-v1.35.0-linux-amd64.tar.gz`. The archive SHA512 matched the official release index:

```
130369c16f076e724d089189afaede960316f5f5dea6cf57be7a4fc6f09c77342893192509790e4056e116e232dff832ed863f5bd55dcb55d38f3ab834828a11
```

An ephemeral etcd 3.6.6 and Kubernetes 1.35.0 API server bound only to dynamically selected `127.0.0.1` ports. One-day private certificates and a task-specific kubeconfig were generated under `/tmp/k9plus-changeset-envtest`; no user kubeconfig, credentials or production destination were read or contacted. The driver used bounded direct HTTPS requests, the same strict field validation, `dryRun=All`, apply content type, manager `k9plus-change-set`, UID and resourceVersion payload fields as the production change-set implementation. Force was omitted. Every child process was terminated and waited in a `finally` block. No workload controller or kubelet ran.

| Case | Actual result |
| --- | --- |
| Strict SSA dry-run carrying current UID/RV | HTTP 200; subsequent GET retained original replicas and resourceVersion |
| Persist exactly the reviewed SSA payload | HTTP 200; independent named GET matched accepted UID/generation |
| Preview / accepted response / independent GET | Full object fingerprints matched after removing only status and server bookkeeping fields used by the production comparator |
| Intervening metadata write, then old SSA payload | HTTP 409 Conflict; reviewed replica update did not persist |
| Delete captured object, then old UID/RV SSA payload | HTTP 409 Conflict; no silent recreation |
| Recreate same name, supply old UID with new current RV | HTTP 422; replacement identity was rejected |
| Strict POST create dry-run | HTTP 201; named GET returned actual API NotFound afterward |
| Persistent POST create and independent GET | HTTP 201; preview, accepted response and GET fingerprints matched |
| POST create against concurrently existing name | HTTP 409 AlreadyExists |
| POST create, then changed SSA using the same manager name | HTTP 409 FieldManagerConflict with the prior `k9plus-change-set` Update ownership on `.spec.replicas` |
| Proposed SSA `resourceVersion: "0"` create-only replacement | Unsafe: HTTP 200 updated an existing object's replicas; this alternative was rejected |

The executable driver and raw case results are retained in the workspace artifact `/workspace/artifacts/reviewed-change-set-envtest-2026-10-04`. They establish API-server semantics for these bounded Deployment cases, rather than a full native-UI end-to-end trial. Mutating webhooks and nondeterministic admission may produce different admitted fields between preview and execution; the production receipt must retain acceptance and report UNKNOWN when its fingerprint checks fail. Runtime adoption, rollout readiness and controller completion remain unverified by this trial.
