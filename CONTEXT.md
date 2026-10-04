# Investigation vocabulary

| Term | Meaning |
| --- | --- |
| Resource identity | The context, API resource kind, namespace, name and observed UID that identify an object. A name alone does not establish continuity. |
| Viewing destination | The active context and namespace shown by the application. |
| Navigation ownership | A page may restore the viewing destination established by its own jump while no later user context or namespace change has superseded it. Changing away and back does not revive that ownership. |
| Observation | Evidence obtained from a named source at a stated time, with its resource identity and completeness. It does not imply desired configuration or health. |
| Snapshot | A retained observation that remains independent of later source changes. |
| Chosen baseline A | The observation the user explicitly chose as the comparison baseline. Obtaining another comparison observation does not replace it. |
| Comparison observation B | An explicitly obtained observation compared with A. B may describe a replacement identity. |
| Recreated identity | A resource with the same context, API kind, namespace and name, but a different known UID. |
| Complete observation | The selected resource content was obtained within the workflow's stated limits. Completeness does not establish health, history or continuity. |
| Denied observation | The source refused access to the requested evidence. |
| Unknown observation | The workflow cannot establish the requested evidence. |
| Incomplete observation | Exclusions, limits or unavailable content prevent a complete retained observation. |
| Stale observation | The requested evidence cannot establish the selected identity as current. |
| Evidence bundle | Portable observations, user notes and explicitly selected snippets, retaining their individual sources, times, identities and limits. It is neither desired state nor an atomic cluster snapshot. |
