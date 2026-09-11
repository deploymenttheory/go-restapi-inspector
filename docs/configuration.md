# Configuration

Pass `--config inspector.yaml`. Scalar CLI settings have `RESTAPI_INSPECTOR_` environment equivalents; dots and hyphens become underscores. Flags override environment, files and defaults. Required settings are checked after merging.

```yaml
spec: vendor.openapi.yaml
base-url: https://your-lab.example/api
operations: [createWidget, patchWidget]
output: inspector-runs
html-report: true
mode: discovery
strategy: active
interaction-order: 3
validation-trials: 3
wait-between-requests: 1s
request-timeout: 30s
max-requests: 0       # unlimited; exhausting a positive cap leaves a partial run
max-duration: 0s
max-planning-tuples: 1000000
boundary-search-steps: 16
confirmation-request-reserve: 0  # estimate automatically; -1 disables; positive sets a count
cleanup:
  attempts: 3
  backoff: 1s
  timeout: 2m
auth:
  type: bearer
  token-env: LAB_API_TOKEN
sensitive-fields: [body:/recoveryCode]
```

Request counts include prerequisites, controls, probes, readbacks, polls and OAuth token requests. Cleanup has its own allowance and deadline. Pacing starts after the response body completes; `Retry-After` can extend it. Mutations are not retried automatically. A later experiment can refresh credentials after a 401.

`html-report` defaults to true, including saved configurations that predate HTML reporting. Set `--html-report=false`, `RESTAPI_INSPECTOR_HTML_REPORT=false`, or `html-report: false` in configuration to disable automatic HTML generation. An explicit `report --run DIR` still renders the saved evidence. See [reporting](reporting.md).

Numeric boundary discovery starts from a valid integer-valued baseline, brackets rejections with at most `boundary-search-steps` exponential probes per direction, and bisects transitions to adjacent integers. Fractional neighbours and accepted integer-string representations are also challenged. Zero disables this search; explicit field domains take precedence. Findings are scoped to these samples and do not prove global monotonicity.

With a request cap, exploration reserves an estimated confirmation allowance, up to one quarter of the cap. An explicit positive reserve overrides that estimate. If exploration pauses, available findings can still be confirmed, and coverage stays partial until the remaining probes run. Confirmation may need more than the estimate, especially when many schema assertions are incorrect.

`resume` checks a fresh baseline and reuses classified logical experiments and completed confirmation pairs. New writes always use fresh fixtures. Increase `max-requests` above the cumulative recorded count to continue. Budgets and discovery settings can change; auth, oracles, hints and other observation context must remain consistent. Redacted requests are never replayed as original values. The supplied config should retain environment references for credentials and sensitive seed values.

`baseline-run` enables incremental `plan` and `inspect` against another explicitly supplied spec. `spec-release` records a readable release label alongside the declared version and content hashes. `evidence-context` identifies the account/tenant configuration revision used for reuse assumptions. See [resume and incremental inspection](incremental-inspection.md) for validation gates, preserved selection scope and evidence lineage.

## Authentication

Credentials use environment-variable references, rather than literal CLI values.

```yaml
auth:
  type: basic
  username-env: LAB_USERNAME
  password-env: LAB_PASSWORD
```

```yaml
auth:
  type: api-key
  token-env: LAB_API_KEY
  name: X-API-Key
  in: header             # header, query or cookie
```

```yaml
auth:
  type: oauth2-client-credentials
  token-url: https://identity.example/oauth/token
  client-id-env: LAB_CLIENT_ID
  client-secret-env: LAB_CLIENT_SECRET
  client-auth-method: client_secret_basic # client_secret_post for credentials in the form body
  token-refresh-buffer: 30s               # use 300s for a five-minute buffer
  scopes: [widgets.read, widgets.write]
```

OAuth client credentials default to HTTP Basic client authentication. Set `client-auth-method: client_secret_post` for form-encoded client authentication. A zero or omitted refresh buffer uses 30 seconds. For other exchanges use an executable provider:

```yaml
auth:
  type: exec
  command: [/absolute/path/credentials, --profile, lab]
```

The process receives JSON on stdin with `version`, `operation`, `baseUrl` and `scopes`. It writes one JSON object on stdout:

```json
{
  "headers": {"Authorization": "Bearer provider-token"},
  "query": {},
  "cookies": {},
  "expiresAt": "2026-09-11T18:00:00Z"
}
```

Arguments execute without a shell. Provider stderr is not recorded. Without `expiresAt`, credentials expire after a minute. `tls.ca-file`, `tls.cert-file` and `tls.key-file` support custom trust and mTLS; TLS verification remains enabled.

Multiple profiles can satisfy OpenAPI security schemes. A requirement object is an AND; objects in the security array are alternatives:

```yaml
auth-profiles:
  LabBearer:
    type: bearer
    token-env: LAB_API_TOKEN
  TenantKey:
    type: api-key
    token-env: LAB_TENANT_KEY
    name: X-Tenant-Key
    in: header
security:
  bearerAuth: LabBearer
  tenantKey: TenantKey
```

An operation hint's `auth: LabBearer` overrides the default selection.

## Baselines and resources

Operation IDs, JSON pointers and profile names retain their case. Field identifiers are `query:limit`, `header:X-Revision`, `path:widgetId`, or body pointers such as `body:/custom/amount`. The representative array object uses index zero: `body:/items/0/name`.

```yaml
hints:
  createWidget:
    role: create
    values:
      body:/name: inspector-fixture
      body:/mode: simple
    value-envs:
      body:/certificatePassword: WIDGET_PASSWORD
    read: getWidget
    delete: deleteWidget
  patchWidget:
    role: update
    bindings:
      path:widgetId:
        operation: createWidget
        pointer: /id
        source: response
    observe:
      body:/displayName: /data/displayName
```

`values` guide baseline generation; mutations can still change those fields. `value-envs` preserves recoverable references to sensitive seed values. Supply the original config on resume if literal seed values were redacted.

Bindings come from OpenAPI links, collection/item paths and identifier schemas. Ambiguous producers need explicit hints. `source` supports `response` (JSON pointer), `header` (header name) and `input` (field identifier). `Location` also helps address an already recorded resource for read/delete. Cycles require a concrete value or revised binding.

Roles are `create`, `update`, `delete`, `read` and `action`. Defaults are POST=create, PUT/PATCH=update, DELETE=delete, other methods=read. Use `action` for non-resource POST operations. Update/delete trials need owned producer fixtures; arbitrary existing IDs do not establish isolation.

## Oracles and asynchronous responses

By default, 2xx is accepted except 202 and recognizable application errors. HTTP 400/422 are input rejections. Auth errors, 404, 409, 412, 429 and 5xx remain inconclusive. Configure API-specific response envelopes globally or in a hint:

```yaml
oracle:
  reject-statuses: [400, 422]
  reject:
    - {pointer: /success, equals: false}
  accept:
    - {pointer: /success, equals: true}
```

Each list uses OR semantics; rejection takes priority. Asynchronous completion requires an explicit polling hint:

```yaml
hints:
  submitJob:
    role: action
    poll:
      operation: getJob
      bindings:
        path:jobId: /jobId
      state-pointer: /state
      success: [completed]
      failure: [failed, canceled]
      timeout: 2m
```

Without this hint, 202 remains inconclusive. Failed jobs are not automatically attributed to request validation. Poll operations should be reads.

## Domains and explicit hypotheses

Discovery challenges omission, presence, enum alternatives, booleans, null, alternate types, empty values and documented boundary neighbours. Independent interaction covering uses omission/presence and enum representatives. SAT queries use the wider value domains; both sets appear in the report.

Override a domain globally or with `OPERATION_KEY#FIELD_ID`:

```yaml
mode: exhaustive
domains:
  'POST /widgets#body:/mode':
    - {present: false}
    - {present: true, value: simple}
    - {present: true, value: advanced}
  'body:/certificate':
    - {present: false}
    - {present: true, value: fixture-certificate}
```

Omission differs from present null. Exhaustive enumeration is lazy and visits structurally realizable assignments in these finite domains. Bound path IDs are not arbitrary fuzzing domains.

Explicit hypotheses may exceed the automatic interaction order:

```yaml
rules:
  - operation: createWidget
    kind: fieldRequiredWhenFieldValueIs
    field: body:/certificate
    when:
      op: all
      args:
        - {op: eq, field: 'body:/mode', value: advanced}
        - {op: present, field: 'body:/organization'}
    assert: {op: present, field: 'body:/certificate'}
```

These are candidates, not trusted constraints. Operators include `present`, `absent`, `null`, `eq`, `neq`, `type`, Boolean/group operators, schema bounds, `enum`, `pattern`, `format`, and `le`/`lt`/`ge`/`gt`/`equalFields`.

## Recovery and evidence

The journal is synced before sending operation requests. Cleanup deletes only recorded resources, in reverse dependency order. A leftover child blocks parent deletion. Each cleanup invocation has at most the configured attempts per resource.

Unresolved write intents remain `ambiguous`; they are not replayed or speculatively deleted. Reconcile them with the provider using the journal and start a new inspection. Provider-specific reconciliation is not automated.

Directories use mode 0700 and files use 0600. Known credentials and sensitive field names are redacted; configure API-specific sensitive fields. Redacted observations explain a run but cannot supply plaintext secrets for replay.

OpenAPI paths and component names are metadata, including names such as `token` or `password`; their schema references are preserved while sensitive examples are removed. If an older journal contains redacted schema references, recovery reloads the original source only when its SHA-256 matches the recorded run. The verified document is then journaled so future resumes do not depend on that source file.
