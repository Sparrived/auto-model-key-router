# Hide Visitor UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hide visitor-related TUI content when the optional visitor feature is not installed.

**Architecture:** Keep runtime/config behavior unchanged. Gate only TUI labels, menu items, columns, and log visitor page shortcuts behind `visitor_feature_available()`.

**Tech Stack:** Python, Rich renderables, pytest.

---

### Task 1: Config Editor Visitor UI

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] Write tests asserting provider summaries and key management pages omit visitor text when `visitor_feature_available()` is false.
- [ ] Run focused tests and confirm they fail.
- [ ] Hide visitor columns, menu actions, and status lines when the feature is unavailable.
- [ ] Re-run focused tests and confirm they pass.

### Task 2: Logs Visitor UI

**Files:**
- Modify: `auto_model_key_router/logs_tui.py`
- Test: `tests/test_tui.py`

- [ ] Write tests asserting logs shortcuts/pages omit visitor text when the feature is unavailable.
- [ ] Run focused tests and confirm they fail.
- [ ] Filter visitor stats page and shortcuts behind the feature flag.
- [ ] Re-run focused tests and confirm they pass.

### Task 3: Verification

**Files:**
- Test: `tests/test_tui.py`

- [ ] Run `python -m pytest tests/test_tui.py -q`.
- [ ] Fix only regressions from visitor UI hiding.
