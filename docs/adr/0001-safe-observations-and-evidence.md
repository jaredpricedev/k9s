<!-- Modified for k9+; see NOTICE. -->

# 0001: Share sanitized observations between comparison and evidence bundles

Status: Accepted

## Context

Resource comparison and portable evidence both retain resource content beyond an
API call. Keeping arbitrary object maps or YAML would expose Secret values,
credential-bearing annotations and command arguments through the screen,
clipboard or exported files. Inferring a desired source from an API resource
would also confuse observed state with intent. Exported evidence needs a stable
identity, source, time and completeness vocabulary that offline readers can trust.

## Decision

Use one observation model for both workflows. Every observation names its context,
GVR, namespace, name, UID, source, observation time and state. The safety boundary
creates an independent sanitized projection, applies bounded retention and reports
exclusions and heuristic redaction. Secret content is excluded by default.

Comparison captures A once and obtains B only through an explicit user action.
Normalization hides a documented, reversible list of API bookkeeping paths. A
replacement UID is labelled as a recreated identity. Denied, stale, unknown and
incomplete inputs prevent a claimed no-change comparison.

Version 1 evidence bundles carry these same observations, explicitly supplied
notes and selected snippets. Export uses a previewed sanitized representation and
a new private file. JSON and Markdown encode the same versioned data. Bounded,
validated import is offline inspection; the format contains no apply instruction.
Desired-state comparison requires a future explicitly chosen and obtained source.

## Consequences

The default report intentionally loses credential-shaped fields, command arguments
and Secret content. Users can inspect the remaining evidence without accidentally
using a raw-object export path. Redaction remains heuristic and is labelled as
such. Increasing retention or adding raw capture requires a separate decision.

Shared source and completeness labels keep comparison and exported evidence
consistent. Consumers must handle unavailable observations and version mismatches,
and cannot treat a bundle as an atomic cluster snapshot or complete history.

The reversible noise toggle makes an API-to-API comparison useful while retaining
an explanation for hidden differences. Desired configuration and live observation
remain different sources in the domain model.

## References

- [Resource comparison](../resource-comparison.md)
- [Evidence bundle format and limits](../evidence-bundles.md)
