# Codex Model-Only Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Codex one-click setup update only model-routing fields and `OPENAI_API_KEY`, preserving every unrelated user setting.

**Architecture:** Keep the existing backup and atomic-write pipeline. Narrow `_configure_codex` to whitelist assignments and mutate the existing OpenAI provider table in place; pass existing auth bytes into `_configure_codex_auth` so it can validate and incrementally update the JSON object.

**Tech Stack:** Python, tomlkit, json, pytest

---

### Task 1: Protect Existing Codex Settings

**Files:**
- Modify: `tests/test_agent_config.py`
- Modify: `auto_model_key_router/agent_config.py`

- [ ] Write failing tests for unrelated TOML, provider, and auth fields.
- [ ] Run `python -m pytest tests/test_agent_config.py -q` and verify failure.
- [ ] Implement field-whitelist TOML updates.
- [ ] Implement incremental `auth.json` updates and validation.
- [ ] Run focused tests and verify success.

### Task 2: Document Minimal Codex Writes

**Files:**
- Modify: `docs/USAGE.md`

- [ ] Remove unrelated fields from the Codex example.
- [ ] Document preservation guarantees.
- [ ] Run `python -m pytest -q` and verify the full suite passes.
