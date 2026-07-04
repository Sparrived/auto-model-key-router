# TUI Choice Inputs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace avoidable free-text TUI prompts with single-choice or multi-choice menus.

**Architecture:** Reuse existing `select_option` and `select_multiple` from `auto_model_key_router/tui.py`. Keep unavoidable arbitrary text prompts for secrets, IDs, URLs, paste JSON, host, and port.

**Tech Stack:** Python, Rich TUI helpers, pytest.

---

### Task 1: Choice Prompt Helper

**Files:**
- Modify: `auto_model_key_router/tui.py`
- Test: `tests/test_tui.py`

- [ ] Add a failing test showing `prompt_text(..., choices=[...])` delegates to a menu instead of manual typing.
- [ ] Run `pytest tests/test_tui.py::test_prompt_text_choices_use_select_option -q` and confirm it fails.
- [ ] Implement the minimal delegation in `prompt_text` before the raw input loop.
- [ ] Re-run the focused test and confirm it passes.

### Task 2: Multi-Select Existing Models

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Test: `tests/test_tui.py`

- [ ] Add failing tests for local route model ID selection and target upstream model selection.
- [ ] Run the focused tests and confirm they fail.
- [ ] Replace eligible model ID text prompts with `select_option` menus plus custom fallback only when needed.
- [ ] Re-run focused tests and confirm they pass.

### Task 3: Final Verification

**Files:**
- Test: `tests/test_tui.py`

- [ ] Run `pytest tests/test_tui.py -q`.
- [ ] Fix only regressions caused by these changes.
