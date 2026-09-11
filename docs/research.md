# Research and adopted ideas

These projects informed the design. Their implementations are not copied or wrapped by this project.

| Source | Idea adopted | Boundary |
| --- | --- | --- |
| [Traffic2OpenAPI](https://github.com/grokify/traffic2openapi) | Separate observations from schema projections | Passive frequency does not prove requiredness; traffic ingestion is future work |
| [Schemathesis](https://github.com/schemathesis/schemathesis) | Positive/negative variation, feedback and reduced counterexamples | Bug discovery differs from contract discovery |
| [RESTest](https://github.com/isa-group/RESTest), [IDL](https://github.com/isa-group/IDL) | Requires, OnlyOne, ZeroOrOne, AllOrNone and relational vocabulary | Supplied constraints are distinct from acquired constraints |
| [RestTestGen](https://github.com/SeUniVr/RestTestGen) | Operation dependencies, missing-field mutations and response values | Description-derived claims remain hypotheses |
| [RESTler](https://github.com/microsoft/restler-fuzzer) | Producer/consumer dependencies and resource sequences | Resource state and object validation are separate concerns |
| [Beet / AGORA](https://github.com/isa-group/Beet) | Candidate invariants and counterexample search | Output invariants do not establish request preconditions |
| [RESTSpecIT](https://github.com/alixdecr/restspecit) | Interrogate a service from incomplete documentation | This implementation uses deterministic generation without an LLM dependency |
| [mitmproxy2swagger](https://github.com/alufers/mitmproxy2swagger), [APIClarity](https://github.com/openclarity/apiclarity) | Preserve evidence alongside reconstructed descriptions | Capture integration is a possible future input source |

The acquisition model follows [version-space constraint acquisition](https://www.ijcai.org/Proceedings/07/Papers/006.pdf): maintain networks consistent with labeled examples and seek distinguishing queries. The server provides a whole-request membership oracle; results requiring a partial-assignment oracle are not assumed to transfer.

Covering arrays provide independent exploration, not causal proof. Delta debugging reduces changes while preserving rejection. These complement SAT model discrimination.

## Native dependencies

- [Cobra](https://github.com/spf13/cobra) and [Viper](https://github.com/spf13/viper): commands and configuration. Case-sensitive API data maps bypass Viper's recursive key normalization.
- [libopenapi](https://github.com/pb33f/libopenapi): OpenAPI parsing/checks alongside a preserved tree for controlled edits.
- [gophersat](https://github.com/crillab/gophersat): pure-Go SAT without an external executable or CGO.
- [jsonschema](https://github.com/santhosh-tekuri/jsonschema): independent validation of exported semantics.
- [OpenAPI 3.1.1](https://spec.openapis.org/oas/v3.1.1.html) and [JSON Schema 2020-12](https://json-schema.org/draft/2020-12/json-schema-validation): standard output, with evidence and nonlocal relationships in extensions.

Further work should compare semantic accuracy and request cost across strategies against exhaustive small ground truths, extend state models for upserts/consistency, and accept traffic-derived seeds. Each addition must preserve the distinction between a hypothesis, scoped reproducible evidence, and a universal claim.
