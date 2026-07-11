# Provider Key Pool CLI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the manual, cross-cutting Key/pool CLI with a provider-first workflow that automatically probes, groups, merges, and cleans up model pools.

**Architecture:** Keep the version 3 configuration and runtime routing contracts unchanged. Add small deterministic helpers for capability probing, pool matching, naming, merging, and cleanup; compose them from provider-scoped TUI workflows so configuration is mutated in memory and committed only after the user confirms a complete valid result.

**Tech Stack:** Python 3.12, httpx, Rich terminal UI, pytest.

---

### Task 1: Preserve `/models` Probe Failures

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing probe-result tests**

Add tests for a result-returning discovery helper that distinguishes a successful empty model list from HTTP/network/JSON failure and includes a concise error string. Add a pool capability test that returns `models`, `all_models`, `key_models`, and `errors` keyed by Key name.

- [ ] **Step 2: Verify RED**

Run the new pytest nodes and confirm they fail because the current helper collapses all failures to `[]`.

- [ ] **Step 3: Implement the minimal result API**

Add:

```python
def discover_upstream_models_result(...) -> tuple[list[str], str | None]:
    ...

def discover_upstream_models(...) -> list[str]:
    return discover_upstream_models_result(...)[0]
```

Use the result API in a provider-Key capability helper. Do not change callers that only need the legacy list behavior.

- [ ] **Step 4: Verify GREEN**

Run the new tests and all existing pool probe tests.

### Task 2: Deterministic Pool Grouping And Merge

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing pure-logic tests**

Cover exact model-set matching, automatic names (`default`, then first free `pool-N`), and duplicate-pool merge. Merge assertions must verify preserved first pool name, Key order, `models` union, latest probe metadata, target redirection, duplicate removal, and no empty model targets.

- [ ] **Step 2: Verify RED**

Run the new nodes; expect missing helpers.

- [ ] **Step 3: Implement pure helpers**

Implement helpers equivalent to:

```python
def next_pool_name(pools: dict[str, Any]) -> str: ...
def matching_pool_names(pools: dict[str, Any], models: list[str]) -> list[str]: ...
def merge_provider_pools(data, provider_id, pool_names, probe) -> str: ...
def cleanup_empty_pools_and_models(data, provider_id) -> set[str]: ...
```

Keep these free of terminal prompts and file writes.

- [ ] **Step 4: Verify GREEN**

Run grouping/merge tests plus config parsing tests.

### Task 3: Automatic Provider-Scoped Add-Key Workflow

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing workflow tests**

Test provider-scoped addition for: unique exact match, no match with automatic pool creation, duplicate matches with merge, and any probe failure falling back to manual existing/new pool selection. Assert cancellation leaves the file byte-for-byte unchanged and new models remain unchecked by default.

- [ ] **Step 2: Verify RED**

Run the workflow nodes; current code should fail because it asks for a pool before probing.

- [ ] **Step 3: Implement the workflow**

Change the entry point to accept an optional provider context:

```python
def add_provider_key_interactively(path: Path, provider_id: str | None = None) -> Any:
    ...
```

Probe the new Key and every existing pool before assignment. Auto-match only when every relevant probe succeeds; otherwise render status-marked manual choices. Prompt for enabled models after assignment, validate with `RouterConfig.from_dict`, then commit once.

- [ ] **Step 4: Verify GREEN**

Run all add-Key, pool probe, cancellation, and configuration-service tests.

### Task 4: Provider-First Navigation And Reduced Menus

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Modify: `auto_model_key_router/dashboard.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing menu tests**

Assert the dashboard options are `一键配置`, `供应商`, `模型设置`, `统一模型`, `调用日志`, `CLI 设置`, and `退出`. Selecting `供应商` opens provider selection, and a selected provider offers `添加 Key`, `管理 Key`, `模型池`, `Base URL / 路由设置`, and `删除供应商`. Assert Key management no longer exposes replacement and pool management no longer exposes refresh or delete.

- [ ] **Step 2: Verify RED**

Run menu tests; expect old labels and options.

- [ ] **Step 3: Implement provider context navigation**

Add provider-scoped key/pool management parameters so nested screens never reselect the provider. Retain model settings as a separate peer entry. Remove the replacement-Key branch, refresh branch, and explicit pool-delete branch.

- [ ] **Step 4: Verify GREEN**

Run menu, dashboard, provider-Key, and model-settings TUI tests.

### Task 5: Pool Details, Rename, Move, And Automatic Cleanup

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] **Step 1: Write failing management tests**

Cover pool details, model selection, rename with target redirection, and manual Key movement with automatic probing. Moving or deleting the final Key must remove the empty pool, redirect or remove targets as appropriate, remove local models with no targets, and update/fallback `unified_model` consistently.

- [ ] **Step 2: Verify RED**

Run the new management tests and confirm missing/old operations fail.

- [ ] **Step 3: Implement the reduced pool manager**

Expose only details, enabled models, rename, and move. Reuse `cleanup_empty_pools_and_models` from Task 2 after move and existing Key deletion. Do not add a separate empty-pool deletion action.

- [ ] **Step 4: Verify GREEN**

Run all provider pool, Key deletion, unified-model, and route-management tests.

### Task 6: Documentation And Regression Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/USAGE.md`
- Modify: `CHANGELOG.md`
- Test: full suite

- [ ] **Step 1: Update user documentation**

Document provider-first navigation, automatic pool matching/merge, failure fallback, automatic cleanup, and removed refresh/replacement actions. Remove instructions that describe obsolete menu paths.

- [ ] **Step 2: Run focused verification**

Run `python -m pytest tests/test_config_service.py tests/test_main.py tests/test_tui.py -q` and expect zero failures.

- [ ] **Step 3: Run full verification**

Run `python -m pytest -q`, `python -m compileall -q auto_model_key_router tests`, and `git diff --check`; expect successful exit codes and a clean diff check.
