# Model Route Responsibility Cleanup Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the TUI treat local model configuration as alias mapping plus routing mode, and keep provider/upstream details in provider-side screens.

**Architecture:** Keep config schema and runtime routing unchanged. Only adjust dashboard summaries and TUI menus so model-facing screens hide upstream/native/reasoning target-management details.

**Tech Stack:** Python, Rich TUI, pytest.

---

### Task 1: Dashboard Model Config Table

**Files:**
- Modify: `auto_model_key_router/dashboard.py`
- Test: `tests/test_tui.py`

- [ ] Add a failing test asserting the dashboard table is titled `模型配置` and omits `推理强度`, `上游`, and `原生支持`.
- [ ] Run the focused test and confirm it fails.
- [ ] Remove upstream/native/reasoning columns from the dashboard model table.
- [ ] Re-run the focused test and confirm it passes.

### Task 2: Provider Model Menu

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] Add a failing test asserting the provider menu no longer exposes `管理模型路由`, `模型参数`, or `供应商路径`, and instead exposes `模型映射`.
- [ ] Run the focused test and confirm it fails.
- [ ] Rename model settings to model mapping and keep only alias plus routing mode.
- [ ] Re-run focused tests and confirm they pass.

### Task 3: Verification

**Files:**
- Test: `tests/test_tui.py`

- [ ] Run `python -m pytest tests/test_tui.py -q`.
- [ ] Fix only regressions from this TUI responsibility cleanup.
