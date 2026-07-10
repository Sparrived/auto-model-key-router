# Key Runtime Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep Key health as an internal bounded-cooldown detail, remove its public runtime management surface, and establish one application runtime with an independent endpoint capability cache.

**Architecture:** Keep the existing lease mechanism only for safe resource retirement during hot reload, but make `RuntimeManager.current` the sole resource source. Keep Key health in memory behind `KeyPool`; persist only endpoint capabilities in their own store.

**Tech Stack:** Python 3.11+, asyncio, FastAPI, httpx, pytest/anyio

---

### Task 1: Define failure behavior

**Files:** `tests/test_key_pool.py`, `auto_model_key_router/key_pool.py`

- [ ] Add regression tests for no automatic disable and bounded cooldown.
- [ ] Run focused tests and confirm they fail.
- [ ] Remove automatic disable and implement bounded cooldown.
- [ ] Run focused tests and confirm they pass.

### Task 2: Separate state responsibilities

**Files:** `auto_model_key_router/key_health.py`, `auto_model_key_router/endpoint_capabilities.py`, `auto_model_key_router/endpoint_capability_store.py`, `auto_model_key_router/key_pool.py`, `tests/test_key_pool.py`

- [x] Add black-box tests for cooldown routing behavior.
- [ ] Move Key health mutation and snapshots into `KeyHealthStore`.
- [ ] Move native endpoint TTL state into `EndpointCapabilityCache`.
- [x] Keep the legacy endpoint cache schema readable.
- [ ] Run Key pool and persistence tests.

### Task 3: Establish one runtime source

**Files:** `auto_model_key_router/runtime.py`, `auto_model_key_router/app.py`, `auto_model_key_router/management_api.py`, `tests/test_runtime.py`, `tests/test_app.py`

- [ ] Remove mirrored app-state resource synchronization.
- [ ] Route all resource access through `RuntimeManager` leases/current.
- [ ] Update hot reload to construct and replace one runtime snapshot.
- [ ] Run runtime, app, and management API tests.

### Task 4: Remove probe lifecycle

**Files:** `auto_model_key_router/app.py`, `docs/USAGE.md`, `router-config.example.json`, `tests/test_app.py`

- [ ] Replace the health-probe test with request-driven recovery coverage.
- [ ] Remove the background probe task and probe helpers.
- [ ] Keep parsing the old interval field as a no-op compatibility setting.
- [x] Document request-driven recovery and config-only enablement.
- [ ] Run focused app tests.

### Task 5: Verify the refactor

- [ ] Run focused Key/runtime/app/management tests.
- [ ] Run the complete test suite.
- [ ] Run configured formatting and linting.
- [ ] Inspect the diff for accidental edits to existing TUI work.
