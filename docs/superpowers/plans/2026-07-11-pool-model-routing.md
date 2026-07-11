# Pool Model Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make pool-enabled models authoritative for routing, enforce one pool per provider Key, and provide accurate interactive pool assignment and model selection.

**Architecture:** Validate and compile pool constraints once in `RouterConfig.from_dict`, leaving `KeyPool` unchanged. Keep probe results separate from user-selected `models`; add only the minimum TUI state needed for checked values and a raw-config repair step before the default interactive CLI loads strict configuration.

**Tech Stack:** Python 3.12, dataclasses, Rich terminal UI, pytest.

---

### Task 1: Enforce Runtime Pool Semantics

**Files:**
- Modify: `auto_model_key_router/config.py`
- Test: `tests/test_config_service.py`

- [ ] **Step 1: Write failing parser tests**

Add tests that build two pools under one provider, enable `gpt-5.6` only in pool B, target both pools, and assert `ModelConfig.keys` contains only B's Key. Add cases asserting missing and empty pool `models` produce no keys, and duplicate Key membership raises a `ValueError` containing the provider, Key, and pool names.

- [ ] **Step 2: Verify RED**

Run `pytest tests/test_config_service.py -q`; expect the new filtering and duplicate-membership assertions to fail.

- [ ] **Step 3: Implement minimal parsing changes**

Parse each pool's `models` as a strict tuple, reject non-list values, track each provider Key's pool owner, and include the enabled model set in the internal `provider_pools` lookup. Before expanding a target, use:

```python
if upstream_model not in pool_models:
    continue
```

- [ ] **Step 4: Verify GREEN**

Run `pytest tests/test_config_service.py -q`; expect all tests to pass after fixtures that require routable keys explicitly declare pool `models`.

### Task 2: Represent Existing Multi-Select State

**Files:**
- Modify: `auto_model_key_router/tui.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write a failing initial-selection test**

Drive `select_multiple` with a stubbed `read_key_responsive` that immediately presses Enter and call:

```python
select_multiple("模型池", [("a", "a"), ("b", "b")], checked_values={"b"})
```

Assert the result is `["b"]`.

- [ ] **Step 2: Verify RED**

Run the single new pytest node; expect `TypeError` because `checked_values` is unsupported.

- [ ] **Step 3: Add the optional argument**

Add keyword-only `checked_values: set[str] | None = None` and initialize checked indexes from matching option values. Existing callers retain the empty-selection default.

- [ ] **Step 4: Verify GREEN**

Run the new test and existing TUI selection tests; expect PASS.

### Task 3: Separate Probe State From Enabled Models

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing probe and display tests**

Assert `apply_pool_probe` preserves existing `pool["models"]`. Assert the model selector passes all discovered models plus retained enabled models to `select_multiple`, passes current models through `checked_values`, labels retained unavailable models with Rich warning markup, and saves the returned values.

- [ ] **Step 2: Verify RED**

Run the new pytest nodes; expect current probe overwrite and missing checked-state behavior to fail.

- [ ] **Step 3: Implement focused helpers**

Keep `apply_pool_probe` from assigning `pool["models"]`. Add one helper that builds options from `all_available_models ∪ models`, marks `models - available_models` as unavailable, calls `select_multiple(..., checked_values=set(models))`, and writes only the confirmed selection back to `models` through `enable_pool_models`.

- [ ] **Step 4: Verify GREEN**

Run the new tests and pool probe tests; expect PASS.

### Task 4: Assign Every New Key To One Pool

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing add/edit tests**

Test adding a Key to a selected existing pool and to a newly named pool. Assert no `default` pool is synthesized, the Key appears in exactly one pool, probing runs for that pool, and the enabled-model selector runs afterward. Test editing pool membership moves a selected Key out of its former pool.

- [ ] **Step 2: Verify RED**

Run these nodes; expect the current unconditional `default` assignment and duplicate membership to fail.

- [ ] **Step 3: Implement assignment and move behavior**

Use `select_or_enter_pool_name` during Key creation, create the chosen pool with `models: []` if needed, add the Key, probe, then show model selection. When saving pool keys, remove selected keys from every sibling pool before assigning them to the current pool.

- [ ] **Step 4: Verify GREEN**

Run the add-Key and pool-management test groups; expect PASS.

### Task 5: Repair Duplicate Membership On Interactive Startup

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Modify: `auto_model_key_router/main.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing repair tests**

Create raw configuration with one Key in two pools. Assert the repair helper asks for a retained or new pool, removes the Key from all old pools, adds it once to the chosen pool, saves the file, and leaves valid configuration unchanged. Assert strict `RouterConfig.load` still rejects duplicates without repair.

- [ ] **Step 2: Verify RED**

Run the repair tests; expect missing helper behavior to fail.

- [ ] **Step 3: Implement repair and startup hook**

Add raw-data duplicate detection and `repair_duplicate_pool_memberships_interactively(path)`. In `main`, call it only when none of the argparse action flags are set and before `RouterConfig.load`; all noninteractive paths rely on strict validation and receive the explicit error.

- [ ] **Step 4: Verify GREEN**

Run repair tests and CLI-related tests; expect PASS.

### Task 6: Regression Verification

**Files:**
- Modify only test fixtures whose intended routes now require explicit pool `models`.

- [ ] **Step 1: Run focused suites**

Run `pytest tests/test_config_service.py tests/test_tui.py tests/test_key_pool.py tests/test_routing.py -q`; expect PASS.

- [ ] **Step 2: Run formatting and static checks**

Run `python -m compileall -q auto_model_key_router tests` and `git diff --check`; expect no output and exit code 0.

- [ ] **Step 3: Run the complete suite**

Run `pytest -q`; expect all tests to pass.
