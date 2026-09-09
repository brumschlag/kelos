# Generic Webhook TaskSpawner Example

This example demonstrates how to drive a TaskSpawner from an arbitrary HTTP
POST source — anything that can deliver a JSON payload (Sentry, Notion,
Slack, Drata, PagerDuty, internal services). Unlike the GitHub and Linear
webhook sources, the generic webhook has no built-in knowledge of any
particular schema; you describe how to extract fields and what to filter on
using JSONPath expressions.

This example wires up Sentry error events: every `error`-level event from a
Python, Go, or Node project triggers a Claude Code Task that investigates
the stack trace and opens a PR with a fix.

## Prerequisites

1. **Webhook server**: deploy `kelos-webhook-server` with the generic source enabled
2. **Sender configuration**: a Sentry (or other system's) webhook integration
   pointed at `/webhook/sentry`
3. **Network restrictions**: the generic endpoint is currently
   unauthenticated — see [Webhook Security](#webhook-security)

## Setup

### 1. Enable the generic source

Enable `webhookServer.sources.generic` in your Helm values:

```yaml
# Helm values
webhookServer:
  sources:
    generic:
      enabled: true
      replicas: 1
```

### 2. Configure the sender

Point the upstream system at `https://your-webhook-domain/webhook/sentry`.

For Sentry: Settings → Integrations → Custom Webhook, with the URL above.

> The endpoint does not currently validate signatures, so the webhook
> integration's secret/signing settings have no effect on Kelos. Restrict
> access at the network layer instead — see
> [Webhook Security](#webhook-security).

### 3. Deploy the TaskSpawner

```bash
kubectl apply -f taskspawner.yaml
```

## Configuration Details

### `source`

Lowercase alphanumeric identifier (with optional hyphens). Determines the
webhook URL path: `/webhook/<source>`.

Each TaskSpawner declares one `source`; multiple TaskSpawners can share a
source to fan out a single event into different work streams.

### `fieldMapping`

A map of template variable name → JSONPath expression evaluated against
the request body. Each key becomes `{{.Key}}` in `promptTemplate` and
`branch`. Lowercase `id`, `title`, `body`, and `url` are also exposed under
their canonical uppercase aliases (`{{.ID}}`, `{{.Title}}`, `{{.Body}}`,
`{{.URL}}`) for compatibility with templates written for the GitHub or
Linear sources.

The **`id` key is required** — it is used to derive a stable delivery ID
for deduplication. Task names are built from the TaskSpawner name, source,
and a hashed suffix based on that delivery ID. If the mapped `id` cannot
be extracted from a payload, retries fall back to a request-body hash and
may dedupe inconsistently if the raw payload encoding changes.

Missing fields in the payload produce empty strings rather than errors, so
optional mappings (like `level` here) do not block Task creation. Malformed
JSONPath expressions surface as errors so that broken specs are visible.

### `filters`

A list of conditions that **all** must match for a delivery to trigger a
Task (AND semantics). Each filter has a `field` (JSONPath) and exactly one
of:

- `value` — exact string match against the extracted field value
- `pattern` — Go [regexp](https://pkg.go.dev/regexp/syntax) against the
  extracted field value

When `filters` is empty, every delivery triggers a Task. A filter whose
`field` is missing in the payload fails (the delivery is skipped).

### `excludeFilters`

A list of conditions that reject a delivery when **any** of them matches
(OR semantics). Entries have the same shape as `filters` — a `field`
(JSONPath) and exactly one of `value` or `pattern`.

`excludeFilters` are evaluated after `filters`, so a delivery triggers a
Task only when it matches every entry in `filters` and no entry in
`excludeFilters`. An exclude filter whose `field` is missing in the payload
does not match, so it never excludes the delivery.

This example drops staging events and events whose title starts with
"test", even when they are `error`-level events on a matching platform.

A malformed JSONPath expression in `filters` or `excludeFilters` is a
configuration error: the spawner is skipped for that delivery and the error
is logged, so a broken exclusion never quietly lets deliveries through.

## Template Variables

Generic webhook TaskSpawners have access to:

- `{{.ID}}` / `{{.id}}` — value of the mapped `id` field (required)
- `{{.Title}}` / `{{.title}}` — mapped `title` field (if present)
- `{{.Body}}` / `{{.body}}` — mapped `body` field (if present)
- `{{.URL}}` / `{{.url}}` — mapped `url` field (if present)
- `{{.Kind}}` — always `"GenericWebhook"`
- `{{.Payload}}` — the full parsed JSON body (use it for advanced
  templating: `{{.Payload.data.event.platform}}`)
- Any additional key declared in `fieldMapping` — for example, the
  `level` and `platform` keys in this example are available as
  `{{.level}}` and `{{.platform}}`

## Sample Payload

The example matches Sentry error payloads of this shape:

```json
{
  "action": "created",
  "data": {
    "event": {
      "event_id": "abc123def456",
      "title": "ZeroDivisionError: integer division by zero",
      "level": "error",
      "platform": "python"
    },
    "url": "https://sentry.io/organizations/acme/issues/789/"
  }
}
```

With the configured `fieldMapping`, the spawned Task gets:

- `{{.ID}}` = `"abc123def456"`
- `{{.Title}}` = `"ZeroDivisionError: integer division by zero"`
- `{{.URL}}` = `"https://sentry.io/organizations/acme/issues/789/"`
- `{{.level}}` = `"error"`
- `{{.platform}}` = `"python"`

And both `filters` match (level == "error" and platform matches the
regex), so the Task is created.

## Webhook Security

> [!WARNING]
> **The generic webhook endpoint is currently unauthenticated.** The
> handler accepts any POST that targets `/webhook/<source>` and matches a
> registered TaskSpawner — request signatures are not validated. Per-source
> HMAC validation is tracked in
> [#1040](https://github.com/kelos-dev/kelos/issues/1040).

Until that lands, restrict access at the network layer:

- Use a `NetworkPolicy` to allow ingress only from known sender CIDRs
  (Sentry publishes its egress IP ranges).
- Front the endpoint with an Ingress / Gateway that enforces IP allowlisting
  or mTLS.
- Keep the webhook Service on a private network and avoid `LoadBalancer`
  exposure on the public internet unless ingress is otherwise restricted.

The Helm chart's `webhookServer.sources.generic.secretName` field is
reserved for future HMAC validation; it currently mounts env vars that
no code reads.

## Troubleshooting

- **Tasks not being created** — check the webhook server logs for
  request errors or filter mismatches.
- **`fieldMapping must include an 'id' key`** — the CRD enforces an `id`
  key in `fieldMapping`. Add one whose JSONPath produces a stable,
  unique identifier per logical event.
- **Same event triggering twice** — verify your `id` mapping resolves to
  a stable string. Falling back to body hashing means JSON encoding
  differences (key order, whitespace) defeat dedup.
- **Filter never matches** — if the field in `filter.field` is missing
  from the payload, the filter fails (silent skip). Use `{{.Payload}}`
  in a debug template to see the actual structure.

## Cleanup

```bash
kubectl delete -f taskspawner.yaml
```
