# HTML reporting

Each inspection produces `report.html` beside the observed OpenAPI contract. The file contains its styling, interactive viewer, and recorded redacted evidence. Open it directly with `file://`; it needs no server, credentials, internet connection, or JavaScript package installation. Node.js and Playwright are development dependencies only.

```sh
restapi-inspector report --run inspector-runs/CURRENT
restapi-inspector report --run inspector-runs/CURRENT --compare-run inspector-runs/BASELINE
```

The first command writes `report.html`. The second writes `comparison.html` in CURRENT. Both verify the journal hash chain and read local artifacts without changing either journal or contract. A partial inspection is a valid report input and rendering it exits successfully. Missing or corrupt journals produce an error; a missing or mismatched observed contract produces a visible warning and leaves its schema fragments unavailable.

`inspect`, `resume`, `cleanup`, and `export` generate HTML after the exporter has finalised its determination and saved that report checkpoint. New contracts include `x-observed-behaviour.report: report.html`. Set `--html-report=false` to disable automatic generation. A rendering failure returns an error while preserving the saved contract and analysis determination. Disabling generation does not remove an older HTML snapshot; regenerate it explicitly to refresh it.

## Views

| View | Questions answered |
| --- | --- |
| Overview | Which operations were inspected? Did their models converge? Where did the HTTP requests go? |
| Field findings | What was declared, what was observed, and which requests support each rule? |
| Experiments | What changed from the baseline? Which control, setup, reads, and cleanup belong to this trial? |
| HTTP requests | Which actual dispatches account for the run’s cost, including authentication and unassociated traffic? |
| Schema changes | Which corrections were applied or left unresolved? What do the before/after request schemas contain? |
| Dependencies | What conditional or compound field relationships were supported? |
| Resource cleanup | Which resources were created and was their deletion confirmed? |
| Compare runs | Which findings became supported, changed meaning or status, or stopped being reported? |

Tables share operation/status filters, text search, and 50-row pagination. Request and response payloads are inserted into the page only when opened. Reusable code panels retain formatted JSON, support copying, and show read-before/readback state side by side. Omission remains distinct from JSON `null`; large numeric values are formatted in Go so the browser cannot round them. The toolbar provides system/light/dark themes, printing, and a download of the embedded observed contract as OpenAPI JSON.

The value matrix groups recorded values by request outcome. Its cells are observations within complete requests, not independent claims that the value is always accepted or rejected. Related create/update fields are linked only when the saved configuration records a resource binding. An HTTP 400 validation rejection can be useful evidence, while authentication errors, server failures and unavailable outcomes remain inconclusive. Unsupported rules and unsent experiments are visible separately.

## Request accounting and provenance

Incremental inspections show a baseline schedule in the overview, including spec changes, selected actions, reusable cases and completed trial pairs. Inherited evidence details identify the original run and spec. Snapshot details include canonical/source fingerprints and the pinned parent journal hash. HTTP totals and the latest-session chart count requests sent in the current run; inherited evidence is counted separately. The schedule describes the initial reuse decision, while coverage shows what execution completed. See [incremental inspection](incremental-inspection.md).

Discovery cases come from recorded coverage. Confirmation requests count the `validation` phase, and controls count `control` plus `validation-control`. Total HTTP requests count dispatch journal events, including authentication, prerequisite creation, read-before, readback, polling and cleanup. These quantities describe different layers of the process and should not be added together as though they were independent test counts. The request-cost chart switches between cumulative traffic and the latest resumed session.

New executions record an `experiment-plan` before setup, carry its identifier through HTTP dispatches and resource ownership, and append an `experiment-end` after cleanup. This preserves incomplete attempts as well as successful experiments. An interrupted resumed session with no new analysis checkpoint shows the earlier findings with a visible warning.

Older journals remain supported. The reader uses explicit observation contexts and unambiguous configured producer bindings to recover available associations. It does not invent planning purposes or associate requests by timing. Where a legacy baseline comparison is available, it uses the earliest accepted baseline and labels that limitation. Requests that cannot be associated remain in the complete HTTP ledger. Authentication dispatches do not contain credential response payloads or statuses.

## Reading dependencies and comparisons

The dependency view preserves each predicate as a complete logical group. A condition followed by a requirement is shown as IF / THEN; cardinality groups retain their recorded operator and members. An OR containing a field already known to be unconditionally required is collapsed by default. It does not establish a dependency on the other member. All rules remain available with their full predicates and evidence.

Comparisons identify snapshots by the final journal hash, so an original run and its continuation can share a run ID without being mistaken for the same snapshot. Findings are matched by operation, kind, field, condition, assertion and value. Generated IDs, evidence IDs and trial-count increases do not constitute semantic changes. An unmatched finding is paired as a revision only when its semantic family has one unambiguous counterpart.

The comparison calls out differences in source specification, target, recorded authentication settings, hints/oracles, discovery settings, configured and observed domains, and budgets. Runtime credential values and unrecorded server state cannot be compared. A newly supported finding often reflects additional exploration; a changed finding is not sufficient proof that server behaviour changed.

“Complete” applies to the selected operations and exploration scope. The overview reports unique method/path pairs inspected against the source inventory. Model convergence, interaction completeness and complete finite input enumeration stay separate. Unselected operations and untested values remain unverified before SDK ingestion.

## Implementation and verification

`internal/report` loads verified journal snapshots into a versioned view, independent of the HTTP client and probing engine. Built-in named Go templates share the shell, navigation, cards, badges, tables, filters, pagination, code blocks, evidence panels, schema comparison, charts and dependency gates. One CSS token system controls both themes and responsive/print styles. The viewer creates DOM text nodes from recorded values; executable assets are compiled into the Go binary and no recorded content is treated as HTML.

Redaction is applied to structured values before display formatting. The HTML contains all retained redacted evidence, so its size scales with the run; a comparison embeds both snapshots. Files are written atomically with private permissions. Treat the report as a portable copy of the underlying evidence when sharing it.

Go tests cover accounting, redaction, precision, legacy journals, partial rendering, snapshot comparison, provenance, configuration and artifact preservation on failure. `make report-browser` builds real saved-run fixtures and exercises the production HTML in Chromium through `file://`, including navigation, evidence drill-down, filtering, pagination, themes, mobile layout, hostile recorded strings and contract download. The browser check runs separately in CI and requires no live API.
