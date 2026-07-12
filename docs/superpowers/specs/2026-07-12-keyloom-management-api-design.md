# Keyloom Management API Design

## Goal

Provide Keyloom-compatible management APIs for providers, keys, pools, routes,
probes, and configuration transfer while retaining AMKR's existing configuration
format, migration, atomic persistence, and runtime reload behavior.

## Scope

The API adds provider-native resources backed by the versioned configuration
format:

- `providers[provider_id]` stores `base_url`, `keys`, `pools`, and `routes`.
- `models[model_id].targets` references a provider and pool.
- Existing `/api/models` and `/api/unified-model` writes participate in the
  same revision check so every management write has one concurrency rule.

The management API must never rebuild the configuration into the retired
`models[].keys` layout. It edits the migrated raw document, preserving fields
outside the requested resource.

## API And Concurrency

Every management read returns `config_revision`. The revision is a stable
SHA-256 digest of the migrated raw configuration JSON encoded with sorted keys
and compact separators. Every management write requires the exact current
revision in its body. A mismatch returns `409` before mutation.

Provider, key, pool, and route routes use the requested resource paths. Their
models use `APIModel`, therefore unknown body fields receive FastAPI's `422`
response. Explicit semantic checks return `422` for invalid URLs, empty names,
bad references, and invalid fields; duplicate names return `409`; missing
resources return `404`.

Provider key reads only return name, enabled state, visitor permission, and the
first 12 characters of the SHA-256 API-key fingerprint. No response, raised
error, or log message includes a Key value or Authorization header.

Deleting a key, or changing it from enabled to disabled, is rejected with
`409` when it would leave the provider with no enabled keys.

## Persistence And Reload

All writes use `ConfigService.update` or `ConfigService.commit`. Those paths
migrate and validate the document before `save_config_data` performs the
existing replace-based atomic write. A successful write forces the existing
runtime reload callback and returns the revision of the committed data.

Import validates a complete candidate document before the commit. It only
takes portable fields from the import: `providers`, `models`,
`upstream_routes`, and model-routing-related fields. Machine-local host, port,
local API key, file paths, and timeout/listening settings stay from the local
document. Before committing, it writes a sibling backup using the same atomic
write mechanism; a validation or write failure leaves the active file intact.

Export returns only portable provider, pool, key, route, and model-routing
configuration. It excludes host, port, local authentication, and paths.

## Probes

`POST /api/probes/keys` and `POST /api/probes/pools` create a UUID probe record
in application state and schedule a background task. The task invokes
`config_editor` probe functions in a worker thread; it records only safe result
fields: status, provider, key name, endpoint, discovered models, latency, and
sanitized error text. `GET /api/probes/{probe_id}` exposes pending, running,
complete, failed, and cancelled states. `POST /api/probes/{probe_id}/cancel`
sets the cancellation request and returns the current state.

Only full local authorization can start, inspect, or cancel a probe. Visitor
credentials get the same `401` management denial as other management routes.
Timeout is passed to the existing probing code. The response model never
contains request headers, API keys, or an upstream URL query string.

## Testing

`tests/test_management_api.py` covers provider/key/pool/route CRUD,
fingerprint-only key responses, unknown body fields, status-code behavior,
revision conflicts, preservation of unrelated raw fields, atomic persistence,
and reload invocation. Probe tests cover task creation, visible results,
timeouts, cancellation, visitor denial, and secret redaction. Transfer tests
assert portable export scope and preservation of local settings through import.
