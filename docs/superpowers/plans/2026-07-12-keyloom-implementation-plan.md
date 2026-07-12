# Keyloom Desktop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Keyloom Windows desktop control plane for AMKR while preserving existing CLI, configuration, service, log, and metrics compatibility.

**Architecture:** Extend AMKR with additive provider/pool/route management APIs first. Build Keyloom as a separate Tauri 2 application with a React/TypeScript UI and Rust host; the host discovers or installs AMKR, controls the local process/task, and exposes only safe IPC operations to the UI.

**Tech Stack:** Python/FastAPI/pytest for AMKR; Tauri 2, Rust, React, TypeScript, Vite, Vitest, Playwright, NSIS, bundled Python runtime for Keyloom.

---

## Scope and Order

The work has two repositories/workspaces but one dependency order:

1. AMKR management API extension and compatibility tests.
2. Keyloom desktop shell and AMKR discovery.
3. Service lifecycle, tray, configuration, metrics, logs, and integrations.
4. Private AMKR runtime, installer, update/rollback, and Windows acceptance.

Each task below produces a testable increment and ends with a focused commit. The desktop workspace is assumed to be created separately from this repository at `D:\Code\keyloom`.

## Existing Files to Reuse or Extend

- `auto_model_key_router/management_api.py`: existing model, key, and unified-model endpoints.
- `auto_model_key_router/config.py`: config paths, migration, dataclasses, atomic persistence.
- `auto_model_key_router/config_editor.py`: provider/pool/route data shape and probing behavior.
- `auto_model_key_router/app.py`: `/health`, `/v1/models`, `/metrics`, runtime reload hook.
- `auto_model_key_router/service.py`: process, PID, Windows task, health, and service operations.
- `auto_model_key_router/agent_config.py`: Claude Code and Codex file update/rollback behavior.
- `auto_model_key_router/log_files.py`, `auto_model_key_router/metrics.py`: log and SQLite metric access.
- `tests/test_management_api.py`, `tests/test_app.py`, `tests/test_config_service.py`: established API/config test patterns.

## Task 1: Define Management API Schemas

**Files:**
- Modify: `auto_model_key_router/management_api.py`
- Test: `tests/test_management_api.py`

- [ ] **Step 1: Add response models that never expose upstream secrets**

Add typed serializers for provider, provider key, pool, route, probe result, and `config_revision`. Provider keys must expose `name`, `base_url`, `enabled`, `allow_visitor`, and a 12-character SHA-256 fingerprint only.

- [ ] **Step 2: Add failing schema tests**

Add tests that create a provider key and assert the response contains `api_key_fingerprint` but not `api_key`, and that unknown request fields return `422`.

Run:

```powershell
python -m pytest tests/test_management_api.py -k "provider or pool or route" -v
```

Expected: FAIL because the new models/routes do not exist.

- [ ] **Step 3: Implement the request/response models**

Use the existing `APIModel` extra-forbid convention. Keep IDs and names non-empty, booleans explicit, and route paths normalized through the helpers already in `config.py`.

- [ ] **Step 4: Run the focused tests**

Run the command above. Expected: PASS for schema and secret-redaction tests.

- [ ] **Step 5: Commit**

```powershell
git add auto_model_key_router/management_api.py tests/test_management_api.py
git commit -m "feat: 增加 Keyloom 管理 API 数据模型"
```

## Task 2: Implement Provider, Pool, and Route Endpoints

**Files:**
- Modify: `auto_model_key_router/management_api.py`
- Modify: `auto_model_key_router/config.py`
- Test: `tests/test_management_api.py`

- [ ] **Step 1: Write endpoint contract tests**

Cover:

```text
GET/POST/PUT/DELETE /api/providers
GET/POST/PUT/DELETE /api/providers/{provider_id}/keys
GET/POST/PUT/DELETE /api/providers/{provider_id}/pools
GET/POST/PUT/DELETE /api/routes
```

Assert atomic persistence, hot-reload callback invocation, duplicate-name `409`, missing-resource `404`, invalid URL `422`, and last-enabled-key protection.

- [ ] **Step 2: Verify tests fail**

```powershell
python -m pytest tests/test_management_api.py -k "provider or pool or route" -v
```

Expected: FAIL with missing route responses.

- [ ] **Step 3: Implement provider and pool mutations**

Use the existing raw config migration helpers rather than introducing a second config representation. Every mutation must go through the existing atomic update helper and call `reload_config` after persistence.

- [ ] **Step 4: Implement route mutations and revision checking**

Return `409` when request `config_revision` differs from the current revision. Compute the revision from the canonical serialized config bytes after migration. Preserve unrelated config fields during every mutation.

- [ ] **Step 5: Run focused and regression tests**

```powershell
python -m pytest tests/test_management_api.py tests/test_config_service.py -v
```

Expected: PASS with no regressions in existing model/key/unified-model endpoints.

- [ ] **Step 6: Commit**

```powershell
git add auto_model_key_router/management_api.py auto_model_key_router/config.py tests/test_management_api.py
git commit -m "feat: 提供供应商模型池和路由管理接口"
```

## Task 3: Add Probe and Config Transfer Endpoints

**Files:**
- Modify: `auto_model_key_router/management_api.py`
- Test: `tests/test_management_api.py`

- [ ] **Step 1: Add failing probe and transfer tests**

Test `POST /api/probes/keys`, `POST /api/probes/pools`, `POST /api/probes/{probe_id}/cancel`, `POST /api/config/export`, and `POST /api/config/import`. Include timeout, cancel-before-completion, per-key result, visitor permission, and import-preserves-local-settings cases.

- [ ] **Step 2: Run tests and verify failure**

```powershell
python -m pytest tests/test_management_api.py -k "probe or transfer" -v
```

Expected: FAIL with `404` until the endpoints are registered.

- [ ] **Step 3: Implement probes using existing config editor probe functions**

Return a `probe_id` immediately, execute the probe in an application-owned task, and expose stable result objects with `status`, `provider`, `key`, `endpoint`, `models`, `latency_ms`, and `error`. The cancel endpoint marks the task cancelled and closes outstanding HTTP requests. Never include request headers or secret values.

- [ ] **Step 4: Implement transfer with explicit scope**

Export only transferable model/provider/key data by default. Import merges that data while preserving host, port, local API key, log path, metrics path, and timeout settings. Reject malformed input before writing.

- [ ] **Step 5: Run all backend tests**

```powershell
python -m pytest
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add auto_model_key_router/management_api.py tests/test_management_api.py
git commit -m "feat: 增加探测和配置迁移接口"
```

## Task 4: Scaffold the Keyloom Desktop Workspace

**Files (new workspace `D:\Code\keyloom`):**
- Create: `package.json`, `vite.config.ts`, `tsconfig.json`
- Create: `src/main.tsx`, `src/App.tsx`, `src/styles/tokens.css`
- Create: `src-tauri/Cargo.toml`, `src-tauri/src/main.rs`, `src-tauri/tauri.conf.json`
- Test: `src/App.test.tsx`

- [ ] **Step 1: Initialize Tauri 2 and React**

Use the Tauri React TypeScript template. Production dependencies are React and the Tauri API packages only. Implement the overview chart with React-rendered SVG paths and native pointer events; do not add a chart library. Use Vitest and Testing Library as development dependencies.

- [ ] **Step 2: Add the first failing shell test**

Assert the app renders `概览`, `供应商`, `模型路由`, `活动`, `集成`, and `设置` navigation labels and a disconnected service state.

Run:

```powershell
cd D:\Code\keyloom
npm test -- --run src/App.test.tsx
```

Expected: FAIL until the shell is rendered.

- [ ] **Step 3: Implement the shell and design tokens**

Define spacing, typography, status colors, focus styles, light/dark system themes, and the confirmed sidebar layout. Do not add placeholder marketing content.

- [ ] **Step 4: Run the shell test and Tauri dev build**

```powershell
npm test -- --run src/App.test.tsx
npm run build
cargo test --manifest-path src-tauri/Cargo.toml
```

Expected: all commands PASS.

- [ ] **Step 5: Commit**

```powershell
git add package.json package-lock.json vite.config.ts tsconfig.json src src-tauri
git commit -m "feat: 初始化 Keyloom 桌面应用壳"
```

## Task 5: Implement Rust AMKR Discovery and Safe IPC

**Files:**
- Create: `D:\Code\keyloom\src-tauri\src\amkr\mod.rs`
- Create: `D:\Code\keyloom\src-tauri\src\amkr\discovery.rs`
- Create: `D:\Code\keyloom\src-tauri\src\amkr\client.rs`
- Create: `D:\Code\keyloom\src-tauri\src\commands.rs`
- Test: `D:\Code\keyloom\src-tauri\src\amkr\discovery_tests.rs`

- [ ] **Step 1: Write discovery tests first**

Use temporary directories and fixture configs to test default path, explicit path, non-default host/port, local API key, missing config, malformed config, and service health responses.

Run:

```powershell
cd D:\Code\keyloom
cargo test --manifest-path src-tauri/Cargo.toml discovery
```

Expected: FAIL until discovery functions exist.

- [ ] **Step 2: Implement read-only config discovery**

Return a typed `AmkrInstance` containing config path, base URL, auth mode, metrics path, log path, version/status, and service state. Do not mutate the file during discovery.

- [ ] **Step 3: Implement safe HTTP client and IPC commands**

Expose only typed commands such as `discover_amkr`, `get_health`, `get_metrics`, `start_amkr`, `stop_amkr`, `restart_amkr`, and `read_log_tail`. Keep raw arbitrary URL requests out of the frontend IPC surface.

- [ ] **Step 4: Run Rust and frontend integration tests**

```powershell
cargo test --manifest-path src-tauri/Cargo.toml
npm test -- --run
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add src-tauri
git commit -m "feat: 增加 AMKR 发现和安全 IPC"
```

## Task 6: Build Overview, Metrics, and Activity Views

**Files:**
- Create: `D:\Code\keyloom\src\api\amkr.ts`
- Create: `D:\Code\keyloom\src\features\overview\OverviewPage.tsx`
- Create: `D:\Code\keyloom\src\features\overview\UsageChart.tsx`
- Create: `D:\Code\keyloom\src\features\activity\ActivityPage.tsx`
- Modify: `D:\Code\keyloom\src\App.tsx`
- Test: `D:\Code\keyloom\src\features\overview\OverviewPage.test.tsx`

- [ ] **Step 1: Add failing component tests**

Test disconnected, loading, healthy, empty-metrics, and API-error states. Test that the overview renders a compact unified-model card, data-summary card, selectable request/token/cache series, and a hover detail containing timestamp, requests, tokens, cache tokens, success rate, and latency.

- [ ] **Step 2: Run tests to verify failure**

```powershell
cd D:\Code\keyloom
npm test -- --run src/features/overview/OverviewPage.test.tsx
```

Expected: FAIL until the components exist.

- [ ] **Step 3: Implement typed API hooks and polling**

Poll health every 5 seconds, metrics every 15 seconds, and activity/log data every 2 seconds only while the activity page is active. Cancel timers on unmount and invalidate queries after writes.

- [ ] **Step 4: Implement the confirmed overview**

Use the compact unified model card, separate data-summary card, trend chart with request/token/cache toggles, hover detail, service status, and recent activity. Show real empty states rather than fabricated values.

- [ ] **Step 5: Run component and build checks**

```powershell
npm test -- --run src/features/overview/OverviewPage.test.tsx
npm run build
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add src
git commit -m "feat: 增加 Keyloom 概览和用量视图"
```

## Task 7: Add Service Lifecycle, Tray, and Windows Startup

**Files:**
- Create: `D:\Code\keyloom\src-tauri\src\windows_service.rs`
- Create: `D:\Code\keyloom\src\features\service\ServicePage.tsx`
- Create: `D:\Code\keyloom\src\features\tray\tray.ts`
- Modify: `D:\Code\keyloom\src-tauri\src\main.rs`
- Test: `D:\Code\keyloom\src-tauri\src\windows_service_tests.rs`

- [ ] **Step 1: Test user-level task commands with mocked `schtasks`**

Assert argument lists for install-user, uninstall, start, stop, restart, and status. Assert that UAC escalation is requested only for system-level operations.

- [ ] **Step 2: Implement lifecycle commands**

Use the discovered config path and AMKR executable. Never start a second service when `/health` reports a healthy existing instance. Capture stdout/stderr into Keyloom diagnostics without displaying a console window.

- [ ] **Step 3: Implement tray behavior**

Close-to-tray is default. Tray actions are open, service status, start, stop, restart, and exit. Exiting the app must not stop an independently registered AMKR service.

- [ ] **Step 4: Run Windows-focused checks**

```powershell
cargo test --manifest-path src-tauri/Cargo.toml windows_service
npm test -- --run src/features/service
```

Expected: PASS on Windows.

- [ ] **Step 5: Commit**

```powershell
git add src src-tauri
git commit -m "feat: 增加服务生命周期和托盘控制"
```

## Task 8: Implement Provider, Pool, Key, Route, and Integration Pages

**Files:**
- Create: `D:\Code\keyloom\src\features\providers\ProvidersPage.tsx`
- Create: `D:\Code\keyloom\src\features\providers\ProviderEditor.tsx`
- Create: `D:\Code\keyloom\src\features\routing\RoutingPage.tsx`
- Create: `D:\Code\keyloom\src\features\integrations\IntegrationsPage.tsx`
- Create: `D:\Code\keyloom\src\features\settings\SettingsPage.tsx`
- Test: `D:\Code\keyloom\src\features\providers\ProvidersPage.test.tsx`

- [ ] **Step 1: Add form contract tests**

Cover create/edit/delete provider, key masking and copy, pool membership, route target, routing mode, aliases, unified model switching, probe progress/cancel, visitor access, and API conflict `409` handling.

- [ ] **Step 2: Implement provider and pool workflows**

Use typed API methods only. Keep provider, pool, key, and route forms separate so each save has one mutation and one refresh boundary.

- [ ] **Step 3: Implement model routing and unified model**

Support automatic routing, fixed Key, aliases, fallback targets, reasoning effort, native endpoint preference, and image model mapping exposed by the current AMKR config.

- [ ] **Step 4: Implement Claude Code and Codex integrations**

Call the existing AMKR integration behavior through a dedicated API/IPC boundary. Show current status, preview changed fields, apply, and rollback. Preserve unrelated user settings.

- [ ] **Step 5: Implement settings and transfer**

Expose local API key status, listen host/port, timeout values, config path, export/import, update check, and diagnostic information. Use the explicit transfer scope defined in the API contract.

- [ ] **Step 6: Run UI tests and build**

```powershell
npm test -- --run src/features
npm run build
```

Expected: PASS.

- [ ] **Step 7: Commit**

```powershell
git add src
git commit -m "feat: 增加 Keyloom 配置和集成页面"
```

## Task 9: Package Private AMKR Runtime and Installer

**Files:**
- Create: `D:\Code\keyloom\scripts\prepare-amkr-runtime.ps1`
- Create: `D:\Code\keyloom\installer\nsis\keyloom.nsi`
- Create: `D:\Code\keyloom\src-tauri\src\installer.rs`
- Test: `D:\Code\keyloom\tests\installer-smoke.ps1`

- [ ] **Step 1: Add runtime preparation smoke test**

On a clean Windows test image, assert the packaged runtime can run `python -c "import auto_model_key_router"` without system Python, uv, pipx, or Node.js.

- [ ] **Step 2: Implement runtime packaging**

Bundle the pinned embedded Python runtime and AMKR wheel under the Keyloom private runtime directory. Record AMKR version and hash in `install-state.json`.

- [ ] **Step 3: Implement NSIS detection and silent install**

Detect existing Keyloom, AMKR executable, AMKR config, user task, system task, and WebView2. Preserve existing config. Run user-level install without elevation; request elevation only for the system-task option.

- [ ] **Step 4: Implement update and rollback**

Download to a temporary directory, verify SHA-256, stop only the Keyloom-managed AMKR process, replace the private runtime atomically, and restore the previous runtime on failure.

- [ ] **Step 5: Run installer smoke test**

```powershell
powershell -ExecutionPolicy Bypass -File tests/installer-smoke.ps1
```

Expected: install, detect, start, upgrade, rollback, and uninstall checks PASS.

- [ ] **Step 6: Commit**

```powershell
git add scripts installer src-tauri
git commit -m "feat: 增加 Keyloom 私有运行时和安装器"
```

## Task 10: End-to-End Windows Verification and Release

**Files:**
- Create: `D:\Code\keyloom\tests\e2e\onboarding.spec.ts`
- Create: `D:\Code\keyloom\tests\e2e\existing-amkr.spec.ts`
- Create: `D:\Code\keyloom\tests\e2e\service.spec.ts`
- Create: `D:\Code\keyloom\tests\e2e\config.spec.ts`
- Modify: `D:\Code\keyloom\.github\workflows\release.yml`

- [ ] **Step 1: Add Playwright/Tauri E2E flows**

Cover fresh install, existing CLI config discovery, non-default port, local authentication, service start/stop, overview metrics, provider edit, unified model switch, integration rollback, tray close, and update failure.

- [ ] **Step 2: Run backend regression suite**

```powershell
cd D:\Code\auto-model-key-router
python -m pytest
```

Expected: PASS.

- [ ] **Step 3: Run desktop unit, Rust, build, and E2E suites**

```powershell
cd D:\Code\keyloom
npm test -- --run
cargo test --manifest-path src-tauri/Cargo.toml
npm run build
npm run test:e2e
```

Expected: PASS on Windows 10 and Windows 11 runners.

- [ ] **Step 4: Run accessibility and packaging checks**

Verify keyboard navigation, visible focus, 125%/150% DPI, dark mode, high contrast, installer signing, SHA-256 output, and clean uninstall behavior.

- [ ] **Step 5: Commit release configuration**

```powershell
git add tests .github
git commit -m "chore: 增加 Keyloom Windows 发布验证"
```

## Self-Review Checklist

- [ ] AMKR provider/pool/route API coverage maps to the CLI/TUI requirements.
- [ ] Existing config discovery precedes new installation.
- [ ] Keyloom never returns or logs upstream Key plaintext.
- [ ] User-level startup is the default; system-level startup is explicitly elevated.
- [ ] Overview contains compact unified model, data summary, selectable trend chart, hover detail, and recent activity.
- [ ] CLI and Keyloom writes use atomic persistence and revision conflict handling.
- [ ] No task relies on an unspecified file, function, or placeholder.
- [ ] Backend and desktop test commands are executable on their target workspaces.
