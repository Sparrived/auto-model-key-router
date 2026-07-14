# Keyloom Management API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Keyloom-compatible provider management, asynchronous probes, and safe configuration transfer APIs.

**Architecture:** Extend the existing FastAPI management module and edit the migrated v3 raw configuration only through `ConfigService`. Store short-lived probe records on `app.state`; invoke the existing config-editor probing functions in worker threads. All management writes validate a body revision before mutation and force the existing runtime reload callback after atomic persistence.

**Tech Stack:** Python 3.12, FastAPI, Pydantic, httpx, pytest, anyio.

---

### Task 1: Define Revision And Provider Test Fixtures

**Files:**
- Modify: `tests/test_management_api.py`
- Modify: `auto_model_key_router/management_api.py`

- [ ] **Step 1: Write failing revision and redaction tests**

```python
def test_provider_reads_redact_keys_and_writes_require_revision(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)
    response = run_client(app, lambda client: client.get("/api/providers", headers=AUTH_HEADERS))
    assert response.status_code == 200
    revision = response.json()["config_revision"]
    key = response.json()["providers"][0]["keys"][0]
    assert key["api_key_fingerprint"] == "65bbff9a6cb9"
    assert "api_key" not in key
    conflict = run_client(app, lambda client: client.post(
        "/api/providers", headers=AUTH_HEADERS,
        json={"config_revision": "stale", "id": "b", "base_url": "https://b.test"},
    ))
    assert conflict.status_code == 409
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python -m pytest tests/test_management_api.py::test_provider_reads_redact_keys_and_writes_require_revision -q`

Expected: FAIL because `/api/providers` is not registered.

- [ ] **Step 3: Add revision helpers and response wrapper**

```python
def _config_revision(data: dict[str, Any]) -> str:
    payload = json.dumps(migrate_config_data(data), sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()

def _with_revision(data: dict[str, Any], **body: Any) -> dict[str, Any]:
    return {**body, "config_revision": _config_revision(data)}
```

Read the raw file under `state.config_write_lock`; reject a write unless its
`config_revision` matches before calling `ConfigService.update`. Add the same
field to every existing management write model and read response.

- [ ] **Step 4: Run the test to verify it passes**

Run: `python -m pytest tests/test_management_api.py::test_provider_reads_redact_keys_and_writes_require_revision -q`

Expected: PASS.

### Task 2: Implement Provider, Key, Pool, And Route CRUD

**Files:**
- Modify: `tests/test_management_api.py`
- Modify: `auto_model_key_router/management_api.py`

- [ ] **Step 1: Write failing full CRUD and error tests**

```python
def test_provider_key_pool_and_route_crud(tmp_path: Path) -> None:
    app, _ = create_file_backed_app(tmp_path)
    # Read the returned revision before each write, then create a provider,
    # a Key, a Pool containing that Key, and a Route target.
    # Assert GET/PUT/DELETE responses, duplicate 409, missing 404, invalid
    # base URL 422, unknown-field 422, and that the final raw config remains v3.
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python -m pytest tests/test_management_api.py::test_provider_key_pool_and_route_crud -q`

Expected: FAIL because the provider resource handlers do not exist.

- [ ] **Step 3: Add minimal resource models and handlers**

```python
class ProviderCreate(APIModel):
    config_revision: str = Field(min_length=1)
    id: str = Field(min_length=1)
    base_url: str = Field(min_length=1)

class PoolCreate(APIModel):
    config_revision: str = Field(min_length=1)
    name: str = Field(min_length=1)
    keys: list[str] = Field(default_factory=list)
    models: list[str] = Field(default_factory=list)
```

Use raw `providers` mappings and raw `models` mappings. Validate IDs and URLs,
require every pool key to exist exactly once across its provider pools, validate
route targets against their provider/pool, and retain every unrelated mapping
entry. Serialize provider keys through the fingerprint-only helper.

- [ ] **Step 4: Run CRUD regression tests**

Run: `python -m pytest tests/test_management_api.py -q`

Expected: provider tests pass; adjust existing write tests to include the
revision returned by their preceding read.

### Task 3: Enforce Last-Enabled-Key, Atomic Commit, And Reload

**Files:**
- Modify: `tests/test_management_api.py`
- Modify: `auto_model_key_router/management_api.py`

- [ ] **Step 1: Write failing persistence tests**

```python
def test_provider_key_cannot_remove_last_enabled_key_and_preserves_unknown_fields(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)
    data = json.loads(path.read_text(encoding="utf-8"))
    data["provider_extension"] = {"retain": True}
    path.write_text(json.dumps(data), encoding="utf-8")
    # Disable and delete the only enabled provider Key: both must be 409.
    # Make a successful edit and assert provider_extension is retained.
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python -m pytest tests/test_management_api.py::test_provider_key_cannot_remove_last_enabled_key_and_preserves_unknown_fields -q`

Expected: FAIL until provider Key mutation checks and raw-document editing exist.

- [ ] **Step 3: Implement shared raw-update path**

```python
if action_removes_enabled_key and enabled_key_count(provider) == 1:
    raise ManagementAPIError(409, "供应商至少需要一个启用的 key")
```

Replace `_management_editable_config_data` with a v3-preserving copy that only
migrates and validates; do not remove `providers`. Reuse `ConfigService` for
atomic commits, set `state.config_mtime = -1`, and invoke `reload_config` after
each successful commit.

- [ ] **Step 4: Verify persistence behavior**

Run: `python -m pytest tests/test_management_api.py -q`

Expected: PASS.

### Task 4: Add Asynchronous Safe Probe APIs

**Files:**
- Modify: `tests/test_management_api.py`
- Modify: `auto_model_key_router/management_api.py`
- Modify: `auto_model_key_router/app.py`

- [ ] **Step 1: Write failing probe lifecycle tests**

```python
def test_key_probe_returns_id_results_and_cannot_leak_secret(tmp_path: Path, monkeypatch) -> None:
    app, _ = create_file_backed_app(tmp_path)
    monkeypatch.setattr("auto_model_key_router.management_api.probe_key_availability", fake_probe)
    # POST starts a probe with a revision-independent request, GET polls it,
    # then assert status/key/endpoint/models/latency/error and no secret.
```

Also test timeout forwarding, cancellation, and visitor `401`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `python -m pytest tests/test_management_api.py::test_key_probe_returns_id_results_and_cannot_leak_secret -q`

Expected: FAIL because probe endpoints are absent.

- [ ] **Step 3: Implement bounded probe registry**

```python
record = {"status": "pending", "cancel_requested": False, "results": []}
state.management_probes[probe_id] = record
state.management_probe_tasks[probe_id] = asyncio.create_task(run_probe(...))
```

Initialize the two state mappings in `create_app`. Run existing
`probe_key_availability`/`probe_provider_key_capabilities` with
`asyncio.to_thread`; map `KeyProbeResult` to a whitelist response shape and
sanitize exception text by replacing known Key values and authorization text.
The cancel endpoint cancels the task and reports `cancelled` once observed.

- [ ] **Step 4: Verify the probe suite**

Run: `python -m pytest tests/test_management_api.py -q`

Expected: PASS.

### Task 5: Add Portable Export And Safe Import

**Files:**
- Modify: `tests/test_management_api.py`
- Modify: `auto_model_key_router/management_api.py`

- [ ] **Step 1: Write failing transfer tests**

```python
def test_config_export_is_portable_and_import_keeps_machine_settings(tmp_path: Path) -> None:
    app, path = create_file_backed_app(tmp_path)
    exported = run_client(app, lambda client: client.post("/api/config/export", headers=AUTH_HEADERS))
    assert set(exported.json()["config"]).issubset({"config_version", "providers", "models", "upstream_routes", "routing_mode"})
    # Import a valid portable document with the current revision and assert
    # host, port, local_api_key and paths are unchanged, plus new revision.
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python -m pytest tests/test_management_api.py::test_config_export_is_portable_and_import_keeps_machine_settings -q`

Expected: FAIL because transfer endpoints are absent.

- [ ] **Step 3: Implement portable projection and validated import**

```python
PORTABLE_CONFIG_FIELDS = {"config_version", "providers", "models", "upstream_routes", "routing_mode", "unified_model"}
portable = {name: deepcopy(data[name]) for name in PORTABLE_CONFIG_FIELDS if name in data}
candidate = {**local_data, **portable}
RouterConfig.from_dict(migrate_config_data(candidate))
```

Write a timestamped sibling backup before `ConfigService.commit`; delete no
backup on failure. Return only the new `config_revision` and safe metadata.

- [ ] **Step 4: Verify transfer behavior**

Run: `python -m pytest tests/test_management_api.py -q`

Expected: PASS.

### Task 6: Run Full Verification And Document APIs

**Files:**
- Modify: `docs/API.md`
- Test: `tests/test_management_api.py`

- [ ] **Step 1: Document resource, probe, and transfer endpoints**

Add the new routes, revision requirement, fingerprint-only Key response rule,
and portable import/export scope to the API reference.

- [ ] **Step 2: Run focused and full test suites**

Run: `python -m pytest tests/test_management_api.py tests/test_config_service.py -q`

Expected: PASS.

Run: `python -m pytest -q`

Expected: PASS.

- [ ] **Step 3: Run static checks**

Run: `python -m compileall -q auto_model_key_router tests`

Expected: exit 0 with no output.

Run: `git diff --check`

Expected: exit 0 with no output.
