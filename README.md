# go-restapi-inspector

Discover the observed behavioural contract of a black-box REST API before using its OpenAPI description to generate an SDK. Requires **Go 1.27.0 or later**.

The supplied spec grounds request generation. The inspector establishes a working baseline, changes fields and combinations of fields, distinguishes competing constraint models, repeats matched experiments, and exports OpenAPI 3.1 with evidence.

## Try the local lab

```sh
go build -o bin/restapi-inspector ./cmd/restapi-inspector
go run ./examples/lab
```

In another terminal:

```sh
bin/restapi-inspector plan --config examples/lab/inspector.yaml
bin/restapi-inspector inspect --config examples/lab/inspector.yaml
```

The lab supplies an inaccurate required-field list and response ID type. Its actual rules require a certificate in advanced mode, forbid it in simple mode, and require a password with a certificate. The inspector discovers these relationships and cleans up its widgets.

Each run writes a private directory containing:

| Artifact | Purpose |
| --- | --- |
| `observed-contract-with-the-facts.openapi.yaml` | Operation-specific corrections and versioned `x-observed-behaviour` evidence |
| `evidence.json` | Requests, outcomes, responses and rule evidence |
| `report.json` | Rules, coverage, changes, request counts and remaining resources |
| `journal.ndjson` | Append-only, hash-chained execution and resource journal |
| `report.html` | Offline interactive overview, field findings, experiments, request accounting and evidence |

## Probe a third-party lab

```sh
export LAB_API_TOKEN='your-token'
bin/restapi-inspector inspect \
  --spec vendor.openapi.yaml \
  --base-url https://your-lab.example/api \
  --operation createWidget \
  --auth-type bearer --auth-token-env LAB_API_TOKEN \
  --wait-between-requests 1s
```

`inspect` sends real requests, including writes and prerequisite creation. Use an account and environment where those changes are intended. An explicit base URL is required. `plan` resolves the spec and references without probing the target API.

Authentication supports bearer tokens, basic auth, header/query/cookie API keys, OAuth2 client credentials, executable providers, multiple profiles and OpenAPI security alternatives. Configuration precedence is **flags → environment → file → defaults**. Environment variables use `RESTAPI_INSPECTOR_`, for example `RESTAPI_INSPECTOR_BASE_URL`.

See [configuration](docs/configuration.md) for seed values, producer bindings, application error oracles, polling and cleanup settings.

## Inspect and recover

```sh
bin/restapi-inspector explain 'POST /widgets' --run inspector-runs/RUN_ID
bin/restapi-inspector export --run inspector-runs/RUN_ID
bin/restapi-inspector resume --run inspector-runs/RUN_ID --config original-config.yaml
bin/restapi-inspector cleanup --run inspector-runs/RUN_ID
bin/restapi-inspector report --run inspector-runs/RUN_ID
bin/restapi-inspector report --run inspector-runs/CURRENT --compare-run inspector-runs/BASELINE
```

`export` and `explain` are offline. Resume reconciles recorded resources and starts fresh fixtures for incomplete operations. It never blindly replays a write whose outcome is unknown.

HTML reports are generated automatically after `inspect`, `resume`, `export`, and `cleanup`, including partial results. Open `report.html` directly in a browser. Use `--html-report=false` to disable automatic generation. The `report` command regenerates HTML offline without credentials, API requests, or changes to the journal or contract; comparisons write `comparison.html` in the current run directory. See [reporting](docs/reporting.md) for views, evidence links, and comparison limits.

## Interpret results

Supported rules include requiredness, observed optionality, required-with, conditional requirements, blocked combinations, cardinality groups, mixed input types, discovered numeric boundaries, declared formats/enums, and numeric/date relations. Create, PUT and PATCH retain separate schemas. Related operations can yield `fieldIsRequiredForCreateOnly` and `fieldIsRequiredForUpdateOnly` observations. Read-after-write evidence describes defaults, generated values, mutability, normalization, coercion, omission effects and acceptance without an observable change.

Corrections require reproducible contrasts: three fresh trials by default. Auth errors, rate limits, state conflicts and server errors remain inconclusive. No numeric confidence score is invented.

`modelConverged`, `interactionComplete` and `inputComplete` are separate report fields. Exhaustive mode enumerates configured finite domains; it does not prove completeness over infinite inputs. Untested claims remain unverified. Known disagreements between request classifications and the exported schema are marked unresolved and make the exported report partial. Review these markers before SDK ingestion. Resume retains classified experiments and completed confirmations while using fresh fixtures for new writes.

See [design and current boundaries](docs/design.md) and [research sources](docs/research.md).

## Development

```sh
make verify   # formatting, race tests, vet and build
make lint     # golangci-lint v2, built with Go 1.27+
make vuln     # govulncheck
cd tests/report-browser
npm ci
npm run install-browser
cd ../..
make report-browser  # offline Chromium interaction, rendering and security checks
```

Tests use local HTTP fixtures. The live test is opt-in: set `INSPECTOR_LIVE=1` and `INSPECTOR_LIVE_CONFIG` to an intended disposable lab configuration.
