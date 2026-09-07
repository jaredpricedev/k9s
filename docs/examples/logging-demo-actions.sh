#!/usr/bin/env bash
# Actions affect only the synthetic logging-demo namespace.
set -euo pipefail
case "${1:-help}" in
  rollout)
    kubectl -n logging-demo rollout restart deployment/mixed-logs
    ;;
  replace)
    pod=$(kubectl -n logging-demo get pods -l app=mixed-logs -o jsonpath='{.items[0].metadata.name}')
    kubectl -n logging-demo delete pod "$pod" --wait=false
    ;;
  event)
    pod=$(kubectl -n logging-demo get pods -l app=mixed-logs -o jsonpath='{.items[0].metadata.name}')
    uid=$(kubectl -n logging-demo get pod "$pod" -o jsonpath='{.metadata.uid}')
    now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    cat <<EOF | kubectl create -f -
apiVersion: v1
kind: Event
metadata:
  generateName: log-demo-
  namespace: logging-demo
involvedObject:
  apiVersion: v1
  kind: Pod
  namespace: logging-demo
  name: $pod
  uid: $uid
type: Warning
reason: Unhealthy
message: "Synthetic log workbench test event; no actual probe failure"
source:
  component: log-workbench-demo
firstTimestamp: "$now"
lastTimestamp: "$now"
count: 1
EOF
    ;;
  burst)
    pod=$(kubectl -n logging-demo get pods -l app=mixed-logs -o jsonpath='{.items[0].metadata.name}')
    kubectl -n logging-demo exec "$pod" -- python -c '
import json
with open("/proc/1/fd/1", "w") as out:
    for i in range(4000):
        out.write(json.dumps({"level":"error", "msg":"synthetic load spike", "http":{"status":503}, "trace_id":"burst-demo"}) + "\n")
'
    ;;
  scale)
    replicas=${2:-3}
    if [[ ! $replicas =~ ^[0-9]+$ ]] || ((replicas < 1 || replicas > 35)); then
      echo 'Use a replica count between 1 and 35.' >&2
      exit 1
    fi
    kubectl -n logging-demo scale deployment/mixed-logs --replicas="$replicas"
    ;;
  *)
    echo 'Usage: bash logging-demo-actions.sh {rollout|replace|event|burst|scale [1..35]}'
    ;;
esac
