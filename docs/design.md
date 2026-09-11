# Design and evidence semantics

```text
OpenAPI → inventory → producer bindings → fresh baseline
                                             ↓
                      unary + covering + distinguishing probes
                                             ↓
                      accepted / rejected / inconclusive evidence
                                             ↓
                              candidate constraint networks
                                             ↓
                               repeated matched contrasts
                                             ↓
                      observed OpenAPI + evidence + coverage
```

`internal/spec` retains the original document and exposes operation-local views. External JSON-pointer references are bundled without scanning neighbouring directories. Schema composition guides generation; it never filters experiments as presumed ground truth.

`internal/graph` resolves explicit bindings, OpenAPI links and collection/item identifier relationships. `internal/engine` provisions prerequisites per experiment, uses symbolic IDs for learning, and records actual wire requests separately. Create, PUT and PATCH retain distinct operation/state scopes.

`internal/generate` supplies finite representatives, lazy Cartesian enumeration, covering arrays and delta debugging. Covering runs independently of the current model. Structurally impossible child-present/parent-absent tuples are excluded rather than labeled as server rejections.

`internal/learn` assigns a SAT selector to each candidate rule. Acceptance disables every violated candidate; rejection requires at least one violated candidate to be part of the network. Inconclusive observations add no constraints. A distinguishing query encodes two consistent networks and a full request on which they disagree. Entailment checks whether any consistent network can accept an input violating the proposed rule.

Default automatic arity is three fields: at most two antecedents and a conclusion. If no network explains the evidence, the report records a model gap and attempts to minimize an unexplained failure. Planning failures and cancellation are not convergence proofs.

## Export and meaning

Supported constraints need matched controls, conclusion-scoped differences, semantic entailment over the finite domains, repeated fresh trials, and no accepted counterexample. An accepted omission disproves unconditional requiredness in at least one context; it does not prove optionality in every context.

Local body constraints use `dependentRequired`, `if`/`then`, `not` and standard schema assertions. Cross-location, state and effect rules remain extensions. Shared components are preserved; operation-local schemas prevent create rules leaking into PATCH. Relaxing requiredness traverses contributing `allOf` branches.

Mixed-type acceptance is represented by independent type exclusions. Numeric transition searches contribute falsifiable bounds; bounded integer-string ranges can be projected into JSON Schema patterns. Repeated accepted counterexamples can widen primitive input types and relax contradicted numeric or length assertions. These corrections retain unrelated structural and conditional assertions.

Response shapes widen without inventing required properties or closed enums. Export checks accepted requests against the revised schema and, for operations with only body/path fields, checks that classified rejected bodies are rejected by it. Remaining disagreements are marked unresolved and make the exported report partial. The CLI journals the final export determination so resume and explain see the same status as the report file. Review unresolved findings before generating an SDK.

Read-before/write/read-after evidence distinguishes omitted update fields that are preserved, cleared, reset or removed. Input acceptance and the stored representation remain separate facts.

| Coverage field | Meaning |
| --- | --- |
| `modelConverged` | Candidate networks agree over the reported value domains |
| `interactionComplete` | Planned realizable covering rows were classified using `interactionDomains` |
| `inputComplete` | Every realizable assignment in finite `domains` was classified |
| `state` | The selected strategy finished without known gaps |

A regression test deliberately restricts the language to binary clauses while truth is `a OR b OR c`: seven observations produce a converged wrong model, and an independent eighth probe exposes the missing rule. Auth, resource state, domains and arity remain assumptions after a complete run.

The append-only journal is synced, hash-chained and single-writer locked. Recovery truncates only an incomplete final line and rejects corrupted complete records. Hash chaining detects accidental edits; it is not an attacker-resistant signature. Resume reuses classified logical inputs and complete confirmation pairs under the same observation context, including compatible older journals. A fresh baseline checks current behavior. New mutations receive fresh fixtures; redacted inputs are excluded from reuse. Analysis signatures ensure broader discovery settings trigger new analysis even when an earlier plan completed.

## Current boundaries

- JSON bodies and ordinary parameter serialization are implemented. JSON Patch, multipart/form data and custom body formats need their own mutation models. Cookie credentials work; cookie fields are not discovery domains.
- Nested objects and the first representative array object are explored. Index-specific evidence is not generalized to all elements. Generation is bounded at depth 12, initial strings at 4096 characters and initial arrays at 64 items. Hard patterns or larger valid fixtures may require hints.
- Inventory comes from the supplied spec. Unknown parameter/path discovery and named/dynamic JSON Schema anchor handling are not implemented.
- Documented bounds and nearby values are challenged. Undocumented numeric bounds use a configurable, bounded integer transition search with fractional neighbours; non-monotone or more complex coercion rules can still exceed the candidate language. Enum success samples never imply a closed enum.
- PUT/PATCH are separate operations. Automatic upsert classification, ETag workflows and provider-specific consistency/reconciliation need further state models. An unchanged read representation is not proof of immutability or internal ignoring.
- Polling requires hints. Writes with unknown outcomes require provider reconciliation. No speculative remote ownership is assumed.
- SAT decision calls are synchronous. Cancellation is checked before and after a call, so a difficult individual solve can delay cancellation. A planning memory guard limits candidate/encoding growth.
- Assignments are enumerated lazily, but run evidence is also held in memory. Very large runs should use the available request or duration limits.
- There is no calibrated numeric confidence or universal claim across tenants, versions, privileges, time or infinite domains.

Tests cover exhaustive truth tables, a model-language counterexample, covering strength, omission/null behavior, shrinking, operation-local `allOf` corrections, references, auth renewal, application oracles, pacing, budgets, fresh resources, cleanup and torn journals. The end-to-end fixture compares exported schema semantics against an independent API implementation. Live testing is opt-in.
