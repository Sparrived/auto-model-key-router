# Agent Routing Modes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support unmanaged, AMKR-native, and AMKR-unified-model configuration states for Claude Code and Codex.

**Architecture:** Extend the existing Agent backup state with the applied mode, preserving its atomic write and exact rollback behavior. Native mode writes only endpoint/auth fields and deletes AMKR-injected model fields, so no additional global configuration or model mapping is needed.

**Tech Stack:** Python, tomlkit, pytest, Rich.

---

## File Structure

- `auto_model_key_router/agent_config.py`: mode constants, persisted applied mode, mode-specific mutations.
- `auto_model_key_router/dashboard.py`: mode-selection actions and state display.
- `auto_model_key_router/proxy_handler.py`: actionable unknown-model response.
- `tests/test_agent_config.py`, `tests/test_tui.py`, `tests/test_app.py`: focused regression coverage.
- `docs/USAGE.md`: Agent mode documentation.

### Task 1: Add Mode-Aware Agent Configuration

**Files:**
- Modify: `tests/test_agent_config.py`
- Modify: `auto_model_key_router/agent_config.py`

- [ ] **Step 1: Write failing native-mode tests**

Add imports for `AGENT_MODE_NATIVE` and `AGENT_MODE_UNIFIED_MODEL`. Build a `RouterConfig` with `local_api_key` and a real model but no `unified_model`, then call native configuration for each Agent. Assert Claude retains its base URL and auth token while all four `ANTHROPIC_*MODEL` variables are absent. Assert Codex retains its OpenAI provider and `auth.json` update while `model`, `review_model`, and `model_reasoning_effort` are absent. Assert the returned status mode is native.

```python
native_config = RouterConfig.from_dict({
    "local_api_key": "local-key",
    "models": [{"id": "gpt-native", "keys": [{"name": "main", "api_key": "upstream-key"}]}],
})
configure_agent(CODEX, native_config, mode=AGENT_MODE_NATIVE, target_path=target, backup_path=backup)
configured = tomllib.loads(target.read_text(encoding="utf-8"))
assert "model" not in configured
assert "review_model" not in configured
assert "model_reasoning_effort" not in configured
```

- [ ] **Step 2: Verify RED**

Run `python -m pytest tests/test_agent_config.py -q`. Expect failure because the mode parameter and constants do not exist and `unified_model` is mandatory.

- [ ] **Step 3: Define and persist managed modes**

In `agent_config.py`, add `AGENT_MODE_NATIVE = "native"`, `AGENT_MODE_UNIFIED_MODEL = "unified-model"`, and `SUPPORTED_AGENT_MODES`. Add `mode: str | None` to `AgentConfigStatus` and `AgentConfigResult`. Add `mode: str = AGENT_MODE_UNIFIED_MODEL` to `configure_agent()`, validate it, require `config.unified_model` only for unified mode, and save `"version": 2` plus `"mode": mode` in the existing backup state. Preserve all original-file and extra-target snapshots.

```python
if mode not in SUPPORTED_AGENT_MODES:
    raise AgentConfigError(f"不支持的 Agent 配置模式: {mode}")
if mode == AGENT_MODE_UNIFIED_MODEL and config.unified_model is None:
    raise AgentConfigError(f"请先配置 {UNIFIED_MODEL_ID}，再应用 {agent_display_name(agent)} 路由配置")
```

- [ ] **Step 4: Report managed status without a new state store**

When target and extra-target hashes match, return the saved backup mode. For a matching legacy backup without `mode`, return `AGENT_MODE_UNIFIED_MODEL`; for all unmatched or unavailable backups return `None`.

```python
mode = state.get("mode") if current_is_applied else None
if mode is None and current_is_applied:
    mode = AGENT_MODE_UNIFIED_MODEL
return AgentConfigStatus(agent, target, backup, backup_available, current_is_applied, mode)
```

- [ ] **Step 5: Implement the minimal native mutations**

Pass `mode` to `_configure_claude_code()` and `_configure_codex()`. Unified mode retains present behavior. Native Claude mode uses `env.pop(key, None)` for only `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, and `ANTHROPIC_DEFAULT_OPUS_MODEL`. Native Codex mode uses `document.pop(field, None)` for only `model`, `review_model`, and `model_reasoning_effort`. Keep endpoint, provider, auth, user settings, and custom fields unchanged.

```python
if mode == AGENT_MODE_UNIFIED_MODEL:
    document["model"] = UNIFIED_MODEL_ID
    document["review_model"] = UNIFIED_MODEL_ID
    document["model_reasoning_effort"] = config.reasoning_effort_by_model.get(config.unified_model.model) or "xhigh"
else:
    for field in ("model", "review_model", "model_reasoning_effort"):
        document.pop(field, None)
```

- [ ] **Step 6: Test transitions and legacy state**

Add a unified-to-native-to-rollback test asserting exact original `config.toml` and `auth.json` bytes, then backup removal. Add a matching legacy backup test that removes `mode` from saved JSON and reports unified status. Add an invalid-mode test that raises before target or backup changes.

- [ ] **Step 7: Verify GREEN and commit**

Run `python -m pytest tests/test_agent_config.py -q`; expect PASS. Commit only `auto_model_key_router/agent_config.py` and `tests/test_agent_config.py` using `feat(Agent配置): 支持原生与统一模型路由模式`.

### Task 2: Expose Both Managed Modes in the TUI

**Files:**
- Modify: `tests/test_tui.py`
- Modify: `auto_model_key_router/dashboard.py`

- [ ] **Step 1: Write failing TUI mode-selection tests**

Monkeypatch `select_option` to choose native mode and exit. Stub `configure_agent_interactively` and assert it receives `AGENT_MODE_NATIVE`. Add status-panel assertions for `AMKR 接管：原生模式`, `AMKR 接管：unified-model 模式`, and unmanaged state.

```python
choices = iter(["2", "0"])
modes = []
monkeypatch.setattr(dashboard, "select_option", lambda *args, **kwargs: next(choices))
monkeypatch.setattr(dashboard, "configure_agent_interactively", lambda _path, _agent, mode: modes.append(mode) or "result")
dashboard.manage_agent_config_interactively(config_path, CODEX)
assert modes == [AGENT_MODE_NATIVE]
```

- [ ] **Step 2: Verify RED**

Run `python -m pytest tests/test_tui.py -q`. Expect failure because the page has one apply action and no mode argument.

- [ ] **Step 3: Implement two apply actions and state-aware rendering**

Replace `应用路由配置` with `应用 unified-model 模式` and `应用原生模式`; move rollback to option 3. Pass `AGENT_MODE_UNIFIED_MODEL` or `AGENT_MODE_NATIVE` through `configure_agent_interactively()` to `configure_agent()`. Use `status.mode` in `agent_config_status_panel()` to display the three states, retaining the changed-file warning when hashes no longer match. Unified success text keeps its requested model line; native success text states that the Agent model is user-configured and must exist in AMKR.

- [ ] **Step 4: Verify GREEN and commit**

Run `python -m pytest tests/test_tui.py -q`; expect PASS. Commit only `auto_model_key_router/dashboard.py` and `tests/test_tui.py` using `feat(TUI): 提供Agent路由模式切换`.

### Task 3: Add Missing-Model Guidance and Documentation

**Files:**
- Modify: `tests/test_app.py`
- Modify: `auto_model_key_router/proxy_handler.py`
- Modify: `docs/USAGE.md`

- [ ] **Step 1: Write a failing direct-model response test**

Send `/v1/chat/completions` with a valid local key and `model: "codex-review-model"` that has no configured pool. Assert status 503 and the exact message below.

```python
assert response.json()["error"]["message"] == (
    "模型 codex-review-model 未配置；请先在 AMKR 的模型设置中配置该模型"
)
```

- [ ] **Step 2: Verify RED**

Run `python -m pytest tests/test_app.py -q -k "missing_model_guidance"`. Expect failure because the response currently says only `未配置模型: <model>`.

- [ ] **Step 3: Update the shared KeyError response**

In `_select_key_or_response()` in `proxy_handler.py`, retain logging and the 503 status but use `context.requested_model_id` in the response below.

```python
{"error": {"message": (
    f"模型 {context.requested_model_id} 未配置；请先在 AMKR 的模型设置中配置该模型"
)}}
```

- [ ] **Step 4: Document the three states**

Update the Claude Code and Codex sections in `docs/USAGE.md`: unified mode injects `unified-model`; native mode only injects router/auth setup and removes AMKR model fields; rollback restores exact pre-AMKR files. In native mode, users must add every Agent-selected model, including Codex `model` and `review_model`, to AMKR first.

- [ ] **Step 5: Verify GREEN and commit**

Run `python -m pytest tests/test_app.py tests/test_agent_config.py tests/test_tui.py -q`; expect PASS. Commit only `auto_model_key_router/proxy_handler.py`, `tests/test_app.py`, and `docs/USAGE.md` using `fix(路由): 提示原生模型配置缺失`.

### Task 4: Complete Verification

**Files:**
- Verify: `auto_model_key_router/agent_config.py`
- Verify: `auto_model_key_router/dashboard.py`
- Verify: `auto_model_key_router/proxy_handler.py`

- [ ] **Step 1: Check whitespace and scope**

Run `git diff --check` and `git status --short`. Expect no whitespace errors, with unrelated pre-existing changes untouched.

- [ ] **Step 2: Run the full suite**

Run `python -m pytest -q`. Expect PASS.
