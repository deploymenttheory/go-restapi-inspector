# Resume and incremental inspection

`resume` continues one specification revision in the existing append-only journal. `inspect --baseline-run` creates a new run linked to a verified earlier journal snapshot. Both preserve completed logical cases and independent confirmation pairs when their observation context is compatible.

## Continue an interrupted run

```sh
restapi-inspector resume --run inspector-runs/RUN --max-requests 5000
restapi-inspector resume --run inspector-runs/RUN --spec same.openapi.json
```

An explicit spec from a flag, environment variable or config file is loaded and checked before opening the journal for writing, initializing the API client, or reconciling resources. This also checks a source whose path has stayed the same but whose contents changed. An incompatible revision is rejected with guidance to use `--baseline-run`. With no explicit spec, resume uses the verified embedded document and does not require the original file or URL.

Run metadata records the source SHA-256, an algorithm-versioned canonical fingerprint of the bundled document, OpenAPI dialect, declared `info.version`, and optional `--spec-release`. JSON object order, whitespace and equivalent numeric spellings do not change the canonical fingerprint. Array order and descriptions matter. Referenced-file changes are detected even if the root file's bytes are identical. External reference locations are part of the bundled representation; relocating an external-reference tree can conservatively require a linked run.

Legacy journals retain support. Their available document and source hash establish the identity; legacy observation signatures are verified before migration. Redacted metadata can only be recovered from the matching original source. A supplied original containing redacted examples may not compare equal to a legacy snapshot that lacks an original canonical fingerprint; resume without a replacement uses the saved snapshot. No missing historical metadata is invented.

Complete compatible operations are skipped. Incomplete operations check a fresh accepted baseline, reuse classified logical inputs and complete control/trial pairs, and append remaining experiments. Unsent, inconclusive and redacted inputs are not reusable. An interrupted confirmation control without its trial is not a completed pair. New writes provision fresh fixtures; fixture setup is distinct from repeating create-operation discovery. Unknown write outcomes remain explicit and require reconciliation.

Request budgets on resume apply to cumulative requests in that run, so increase `--max-requests` above the recorded count. Changes to discovery settings can require more work without invalidating otherwise compatible observations.

## Inspect another revision

```sh
restapi-inspector plan --baseline-run inspector-runs/PARENT \
  --spec next.openapi.json --spec-release 11.31.1
restapi-inspector inspect --baseline-run inspector-runs/PARENT \
  --spec next.openapi.json --spec-release 11.31.1
```

The CLI inherits baseline configuration before applying file, environment and flag overrides. A new spec must be explicit. The old release label is not copied. The target must match, and baseline resources must be reconciled before creating a child run.

An explicit operation allowlist is preserved by method, path and request media type. Removed operations stay in history; removing the entire allowlist produces an empty scope, not an all-API inspection. With an all-API baseline, added operations join the selected scope. Use `--operation` to explicitly select a different scope.

`plan` makes no target API requests. Its `baseline.operations` entries distinguish spec change from scheduled action:

| Action | Meaning |
| --- | --- |
| `inherit` | Compatible evidence completes the configured inspection scope. |
| `resume` | Reuse eligible classified cases and confirmation pairs; finish pending work. |
| `inspect` | New, uncovered, invalidated or incompatible work needs inspection. |
| `blocked` | A known path binding or supported request mutation model is missing. |
| `out-of-scope` | The operation was not selected. |
| `history` | The operation is absent from the new spec. |

Each decision includes reasons and counts of reusable logical cases and complete confirmation pairs. A reusable-case count includes distinct classified baselines and trials; it differs from the report's discovery-probe count. These are planning decisions, not final execution counts. Active learning may generate additional experiments from new outcomes, and interrupted execution may leave scheduled work unfinished.

## Change detection and reuse boundaries

Operation identities remain stable across `operationId` renames and adding request media types. Fingerprints cover the effective operation, inherited parameters, security and servers, referenced schemas, descriptions, relevant tag metadata and privilege extensions. Separate request, response, documentation and security digests explain changes. Reference cycles are retained deterministically. Version-label changes and unused components do not invalidate unrelated operations.

Observation compatibility also follows fixture producers, readback, polling and cleanup companions. A producer's response change can invalidate its consumers even if their own schemas are unchanged. Relevant hints, auth profiles, oracles, seed values, TLS certificate content and discovery settings are included at their appropriate scope. Unrelated operation hints and transport budgets do not invalidate an operation's classified probes.

This implementation conservatively invalidates all experiments for a changed operation or changed dependency. That includes field additions/removals, enum/bound/requiredness changes, documentation changes and access metadata changes. Existing experiment plans record changed fields, stable logical case IDs, revision, context and dependencies, but they do not prove the absence of cross-field interactions. We therefore do not narrow changed-operation reuse to a field merely because an old payload omitted that field. An identical payload may now have a different valid outcome and is retested. Unchanged compatible operations receive probe-level deduplication and reuse of completed confirmation pairs.

Spec equality cannot establish unchanged backend behaviour, tenant configuration or privileges. Inherited results explicitly carry that assumption. OAuth client IDs and basic-auth usernames contribute to the context fingerprint; token refresh timing does not. Opaque bearer tokens and executable credential providers do not reliably identify a principal. Set `--evidence-context` to an account/tenant configuration revision and change it when principal, permissions or environmental assumptions change. A changed context schedules reinspection in a linked run and is rejected by same-run resume. Rotating access tokens is not itself a reason to replay completed probes.

## Evidence and exports

The parent journal remains unchanged. The child pins its final journal hash and stores `baseline-journal.ndjson` as portable parent history. A single `inheritance` event contains applicable observations, progress, confirmation evidence and analysis. Each inherited observation, rule and completed coverage entry names its original run, operation and spec identity. Experiment identifiers and evidence identifiers remain intact. A child can be resumed, exported and rendered without the parent directory.

Inherited requests are excluded from the child's HTTP budget and request totals. Parent resources are never imported as child-owned resources. Supporting readback evidence can be retained for explanation without using it as an active fact for another operation's contract. Invalidated rules and observations remain in parent history and cannot constrain the current learner or contract export. Current corrections are rebuilt against the new source document.

The HTML overview shows the incremental schedule and counts; evidence details show original provenance. Snapshot details include the source identity and pinned parent. `report --compare-run` remains an offline findings comparison and does not schedule inspection.

## Validation with the Jamf snapshots

The comparison was verified against SDK commit `2db5b3017fd788d61c2be3bd3f77d94a25e6597c`, using these pinned sources:

- [11.30.2 modern API](https://raw.githubusercontent.com/deploymenttheory/go-sdk-jamfpro-v2/2db5b3017fd788d61c2be3bd3f77d94a25e6597c/openapi-specs/11.30.2-t1785446884863/api-schema.json)
- [11.31.1 modern API](https://raw.githubusercontent.com/deploymenttheory/go-sdk-jamfpro-v2/2db5b3017fd788d61c2be3bd3f77d94a25e6597c/openapi-specs/11.31.1-t1787060595569/api-schema.json)

Store them as `DIR/11.30.2/api-schema.json` and `DIR/11.31.1/api-schema.json`, then run:

```sh
JAMF_SPEC_DELTA_DIR=DIR go test ./internal/spec -run '^TestJamfVersionDelta$' -count=1 -v
```

The production fingerprint implementation finds **3 added, 8 changed, 0 removed and 809 unchanged method/path operations**. Six changes affect privilege metadata; two SMTP operations consume a shared field-description change. Both specs declare `info.version: production`, demonstrating why hashes and explicit release labels are separate. The regression groups request media variants by method/path for these counts; scheduling uses individual representations.

Default tests use local HTTP fixtures with changed server behaviour, including an enum expansion that makes a previously rejected identical request succeed. They cover strict rejection without target requests or journal changes, interrupted discovery, missing confirmation pairs, inherited complete coverage, uncovered endpoints, scope preservation, dependency invalidation, unknown writes, portable reports and resuming a child with its parent offline. These tests do not make requests to Jamf or claim to reproduce historical Jamf server behaviour.
