<!-- Modified for k9+; see NOTICE. -->

# Change and release review

The first Horizon 2 increment adds read-only local manifest review and native
Deployment rollout review. Open either workflow explicitly; ordinary browsing
does not discover providers or collect release evidence in the background.

## Review authored intent

Open `:review /absolute/path/manifest.yaml`, or `:review` to choose a local file.
The file may contain several YAML documents or JSON objects. Save rendered Helm
or Kustomize output to a regular file before opening it; this workflow does not
run Helm, Kustomize, Git or another CLI.

The source observation records the absolute path, SHA256 of its exact bytes,
size and load time. `r` reads live targets again using that same retained source;
editing the file does not silently replace the baseline. `n` explicitly selects
or reloads a source. A failed reload retains the previous source and report.

| Key | Result |
| --- | --- |
| Enter | Open changed fields first, with safe live/authored values |
| `e` | Toggle Evidence/source: full paths, source hash, timestamps, scope and ownership |
| `r` | Refresh named live reads with the retained source |
| `n` | Choose or explicitly reload the source file |
| `/` | Search retained kind, namespace, name and review state |
| `i` | Investigate the selected identity-verified live resource |
| Esc | Return from detail, then return to the previous view |
| Ctrl-O | Search available actions |

Resource detail spends its first viewport on changed values. The compact strip
retains destination, local/read-only status, source fingerprint, observation time
and read gaps. Evidence/source retains full provenance and latest source/read
failures. Enter, `e`, search and Back operate on retained data.

When opened from a daily workspace, review captures the **visible** workspace's
context, namespaces, kind selection, label selector and observed UIDs. A single
namespace supplies an unambiguous default for manifests that omit namespace.
Several namespaces require each manifest to declare its namespace. A normal
browser supplies its explicit namespace and label selector; an all-namespace
browser requires source-explicit namespaces. Context or viewing destination
changes require reopening the review. A retained resource inspector uses its
captured resource namespace rather than another global browsing namespace.

Each target uses exact API-version discovery followed by a named GET. No
resource inventory, Secret body, cluster-scoped target, apply, server dry-run or
prune request is made. Authored kinds and namespaces outside a saved workspace
are marked out of scope. Captured UIDs permit reviewing authored label changes,
but replacement UIDs and disappeared captured resources require reopening.

The local projection compares **provided fields**, retaining omissions. Live
defaults, extra map fields and injected native named-list entries do not become
removal proposals. Native Pod/workload containers, env and volumes match by
unique name, and resource quantities compare their numeric meanings. Other
lists remain whole-value comparisons with merge/replacement semantics explicitly
unreviewed. An explicit null is shown as authored intent, never as resource
deletion. A missing named resource is a create candidate, not an authorization
or admission result. Sensitive values, args and commands are unreviewed rather
than counted as equal.

The report does not evaluate admission, server defaults, managed-field
ownership, apply conflicts, GitOps pruning or omitted resources. Ownership
references and controller tracking labels are reported metadata; they are not
verified ownership decisions. This is separate from `:compare`, which compares
two explicitly captured API observations.

Inputs are bounded to 1 MiB and 64 objects. The parser rejects duplicate keys,
duplicate resource identities, aliases, merge keys, custom tags, List wrappers,
non-JSON-compatible values, symlinks and symlink directory ancestors. Source
errors exclude raw YAML values. Comparisons retain at most 2,000 changes and
use the existing bounded, heuristic credential and terminal-control projection.
Secret documents retain identity metadata only. Heuristic redaction does not
guarantee confidentiality for arbitrary configuration content.

## Review a Deployment rollout

Select a native `apps/v1` Deployment, then open `:rollout` or the Deployment
rollout review action. Its context, namespace, name and UID are captured before
the read. The retained observation separates API progress from child collection
coverage.

| Tab | Retained evidence |
| --- | --- |
| `1` Overview | Generation/observedGeneration, desired/updated/ready/available/total counts and blocking conditions |
| `2` Revisions | UID-owned ReplicaSets, reported revision annotations, counts and template matches |
| `3` Pods | UID-owned Pods, declared image references and observed runtime image IDs |
| `4` Recovery | Comparison with an explicitly selected retained ReplicaSet template |
| `5` Evidence | Source, captured identity, timestamps, conditions and collection limits |

`r` explicitly refreshes. Tab, search, detail and recovery navigation use retained
data. In Revisions, `j`/`k` or arrow keys choose a revision; Page Up/Down move
the selection by a page. The selected row stays visible after navigation and
resize. Enter previews the exact selected UID. Search and `n`/`N` navigate text
matches independently; they never select a recovery target.

Refresh preserves a selected UID when present. If that UID disappears from the
retained set, selection is visibly invalidated and Enter cannot preview a
replacement. Use the selection keys to choose again. Recovery names the
previewed ReplicaSet and keeps it separate from any pending new choice.
Recovery always reports **not executed**. Revision numbers and creation times
do not silently choose a recovery target. A template match identifies retained
equivalence; it does not prove which ReplicaSet the controller selected.

Progress requires controller observation of the current generation before older
conditions become a current verdict. Missing counts remain unknown. A complete
Deployment status does not establish application availability or complete Pod
coverage. Child queries remain in the selected namespace, use the Deployment's
selector, and verify controlling owner UIDs. ReplicaSet and Pod reads are
paginated and bounded; denied or truncated coverage remains visible.

Failed Deployment reads retain the prior snapshot and original capture time.
New partial child evidence has its own capture and coverage. Known identity
changes stop refresh; destination changes cannot redirect a captured reader.
No restart, scale, rollback or Helm operation is submitted by this view.

## Terminal layout

At 80×24, each review uses one main pane with compact metadata. At 60 columns,
manifest rows combine kind/name and retain state; full namespace/identity and
values remain available in detail. The revision list hides secondary counts
into Evidence and keeps its selected row visible. Narrow tabs show the current
label and a Tab cycler. Deliberate truncation uses an ellipsis.

The supported task viewport is at least 40×12, excluding application chrome
(usually a 40×16 terminal in compact mode). Below that size, a resize/back/quit
message replaces clipped content. Returning to a supported size restores the
retained source, query, tab and selection.

## Remaining Horizon 2 work

This increment delivers the local-file foundation of TK06 and read-only native
Deployment evidence of TK07. Named Git and provider sources, authoritative
deletion plans, explicit server dry-runs, accepted-write outcome tracking,
guarded recovery execution and other rollout controllers remain pending.
TK08 GitOps ownership/progress, TK09 reviewed change sets, TK10 configuration
review and TK11 activity remain subsequent roadmap work. Inherited mutation
paths must adopt the destination and operation lifecycle safeguards before
execution is added.

Validation uses bounded fake clients and the running terminal application
against a disposable API. These checks establish request scope and observed
behavior; production controller coverage and operator usability remain separate.
See the [dated validation record](change-release-validation-2026-10-04.md) and
[actual terminal captures](evidence/change-release-review-2026-10-04/README.md).
