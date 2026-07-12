# Agent Routing Modes Design

## Goal

Extend Claude Code and Codex one-click configuration with three distinct states:

- **Unmanaged**: AMKR has not changed the Agent configuration. Choosing rollback restores the exact files that existed before AMKR first applied a mode.
- **AMKR native mode**: AMKR configures only the local routing endpoint and local authentication. The Agent chooses its own model names.
- **AMKR unified-model mode**: AMKR configures the routing endpoint, local authentication, and injects `unified-model` as the Agent model.

Native mode must work when no `unified_model` is configured in AMKR.

## Configuration Behavior

### Claude Code

Both managed modes set the AMKR base URL, local token, and existing AMKR traffic-control settings.

Unified-model mode additionally sets these environment variables to `unified-model`:

- `ANTHROPIC_MODEL`
- `ANTHROPIC_DEFAULT_HAIKU_MODEL`
- `ANTHROPIC_DEFAULT_SONNET_MODEL`
- `ANTHROPIC_DEFAULT_OPUS_MODEL`

Native mode removes those four model variables. It does not choose or add a replacement model; the user configures the Claude Code default model manually.

### Codex

Both managed modes set the AMKR OpenAI provider (`base_url`, Responses wire API, and local-auth requirement) and `auth.json` `OPENAI_API_KEY`.

Unified-model mode additionally sets `model`, `review_model`, and `model_reasoning_effort` for `unified-model`.

Native mode removes those three top-level model fields. It does not infer model names or reasoning effort. The user configures `model`, `review_model`, and any other Codex model fields manually after native routing is enabled.

## State and Backup

Continue using the existing per-Agent backup file and atomic write pipeline. Add a `mode` field to the saved applied state, with values `native` and `unified-model`; retain the original content and extra-file snapshots unchanged.

`get_agent_config_status()` verifies hashes as it does today and reports the saved managed mode only when all managed files still match. A missing, invalid, or nonmatching state is unmanaged. Existing backups lacking `mode` are treated as legacy unified-model applications when their stored applied hashes match, preserving existing rollback behavior without a migration file or global configuration state.

Applying either managed mode over another reuses the original backup only when the current managed files match the previous applied hashes. Otherwise it starts a new exact backup from the current files. Rollback always restores the original snapshot and removes the backup, returning to unmanaged.

## TUI

Each Agent page under **One-click configuration** displays its current state and exposes:

1. Apply unified-model mode.
2. Apply native mode.
3. Roll back original configuration.

The unified action retains its current prerequisite: `unified_model` and a local API key must be configured. Native mode requires only the local API key. The rollback option remains disabled when no matching backup is available.

## Native Request Routing

No router-side model translation is added for native mode. Requests for real configured model IDs and aliases keep their existing routing behavior. This applies independently to Codex's `model`, `review_model`, and other Agent-selected model fields.

When an Agent requests a model that AMKR has not configured, the router error must include the received model name and clearly state that the model must first be added in AMKR's model settings. Existing authentication, protocol, and retry error behavior remains unchanged.

## Testing

- Unified mode preserves existing behavior and records its mode.
- Native mode can apply without `unified_model`.
- Native mode preserves routing/auth configuration and removes only AMKR model-injection fields for each Agent.
- Switching managed modes preserves one original backup and rollback restores exact original files, including Codex `auth.json`.
- Status distinguishes native, unified-model, and unmanaged states, including legacy backups.
- Missing-model errors contain the original requested model name and the AMKR configuration guidance.
- TUI options expose both apply actions and the current state.

## Scope Boundaries

Do not add a separate persistent Agent-mode configuration, model discovery, a mapping layer, or automatic restoration of model fields removed by native mode. The original file snapshot remains the sole source for full restoration.
