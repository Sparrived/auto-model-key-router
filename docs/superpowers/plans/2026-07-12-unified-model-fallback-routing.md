# Unified Model Fallback Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add request-scoped primary/fallback routing for `unified-model` and repair its configuration, interface, and reload inconsistencies.

**Architecture:** Represent each request kind with a primary `RouteTarget` and optional fallback target. Normalize and validate this plan in configuration, resolve it before Key selection, then execute at most two existing Key-routing attempts. Keep Key health and retry behavior per target.

**Tech Stack:** Python 3.11+, FastAPI, httpx, pytest/anyio.

---

### Task 1: Nested unified configuration and migration

**Files:**
- Modify: `auto_model_key_router/config.py`
- Test: `tests/test_app.py`

- [ ] Add failing tests for legacy flat config migration, alias normalization, target validation, and image inheritance.
- [ ] Run the focused tests and confirm the old flat-only configuration fails them.
- [ ] Add `RouteTarget`, `RoutePlan`, nested `UnifiedModelConfig`, migration, canonicalization, and validation.
- [ ] Run focused configuration tests and confirm they pass.

### Task 2: Route-plan resolution and public model listing

**Files:**
- Modify: `auto_model_key_router/proxy_support.py`
- Modify: `auto_model_key_router/key_pool.py`
- Test: `tests/test_key_pool.py`, `tests/test_app.py`

- [ ] Add failing tests for default/image resolution, explicit primary Key override, and local model listing.
- [ ] Run focused tests and confirm they fail because the resolver is absent or has old behavior.
- [ ] Add a request-kind classifier and plan resolver; keep `unified-model` outside ordinary aliases and expose all local callable models.
- [ ] Run focused tests and confirm they pass.

### Task 3: Primary/fallback execution

**Files:**
- Modify: `auto_model_key_router/proxy_handler.py`
- Test: `tests/test_app.py`

- [ ] Add failing proxy tests for primary success, retryable fallback, non-retryable response, explicit Key, image fallback, and fallback response header.
- [ ] Run the focused proxy tests and confirm they fail with the old single-target behavior.
- [ ] Extract the existing per-target attempt loop and execute fallback only after the primary exhausts a retryable failure or has no Key.
- [ ] Run focused proxy tests and confirm they pass.

### Task 4: Unified mutation and management API

**Files:**
- Modify: `auto_model_key_router/unified_model.py`
- Modify: `auto_model_key_router/management_api.py`
- Test: `tests/test_management_api.py`, `tests/test_app.py`

- [ ] Add failing API tests for nested GET/PUT, legacy PUT compatibility, and invalid fallback targets.
- [ ] Run focused API tests and confirm they fail with the flat API response.
- [ ] Centralize nested plan mutation and update management endpoints to read/write the canonical structure.
- [ ] Run focused API tests and confirm they pass.

### Task 5: CLI, TUI, hot reload, and documentation

**Files:**
- Modify: `auto_model_key_router/main.py`
- Modify: `auto_model_key_router/dashboard.py`
- Modify: `auto_model_key_router/app.py`
- Modify: `README.md`, `docs/USAGE.md`, `docs/API.md`, `router-config.example.json`
- Test: `tests/test_tui.py`, `tests/test_main.py`, `tests/test_app.py`

- [ ] Add failing tests for target selection and invalid hot reload retaining the active runtime.
- [ ] Run focused tests and confirm they fail with the old interface/reload behavior.
- [ ] Add target selection UI/CLI, candidate runtime construction guards, and update public documentation/examples.
- [ ] Run focused tests and confirm they pass.

### Task 6: Full verification

**Files:**
- Test: `tests/`

- [ ] Run unified-model, management API, TUI, CLI, KeyPool, and full test suites.
- [ ] Inspect the final diff and confirm only intended changes are staged for a separate commit.
