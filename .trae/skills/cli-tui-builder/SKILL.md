---
name: "cli-tui-builder"
description: "Guides production CLI/TUI design with menus, prompts, logs, config, and validation. Invoke when building or improving terminal workflows."
---

# CLI/TUI Builder

Use this skill when the user asks to build, refactor, or improve a command-line interface or terminal UI, especially when the project needs interactive menus, configuration management, logs, long-running actions, or a polished terminal workflow.

## Core Principles

- Treat the TUI as the main product entry when the user asks for an interactive terminal experience.
- Keep command-line flags for automation, scripts, CI, and direct operations.
- Separate three layers clearly: configuration loading, UI rendering, and business actions.
- Never print secrets unless the user explicitly needs a newly generated value once.
- Prefer in-place rendering for selection UIs; avoid repeated prints that push old screens into scrollback.
- Keep long-running actions out of the TUI foreground unless the user explicitly asks for foreground mode.

## Recommended CLI/TUI Shape

Provide both:

- A default TUI entry:

```bash
python main.py
```

- Scriptable command flags:

```bash
python main.py --show-config
python main.py --show-logs
python main.py --run
python main.py --status
```

The default no-argument path should open the TUI. Flags should bypass the TUI and perform one action directly.

## TUI Menu Design

Use a stable main menu with clear action labels:

- Start long-running action in background
- Stop background action
- Show background action status
- Add or edit configuration
- Generate or rotate local API key
- Show logs
- Refresh configuration
- Exit

Use arrow-key selection for every choice-type interaction:

- Main menu choices
- Submenu choices
- Yes/no confirmation prompts
- Action-management choices

Keep free-text prompts only for actual data input:

- Resource ID or name
- Friendly item name
- Base URL
- Secret value, API key, or token
- File paths, if needed

## In-Place Rendering

For menu selection, use a live/in-place rendering mechanism instead of repeated print calls.

Recommended pattern with Rich:

```python
with Live(render_menu(selected), console=console, screen=True, auto_refresh=False) as live:
    while True:
        key = read_key()
        if key == "up":
            selected = (selected - 1) % len(options)
            live.update(render_menu(selected), refresh=True)
        elif key == "down":
            selected = (selected + 1) % len(options)
            live.update(render_menu(selected), refresh=True)
        elif key == "enter":
            return options[selected]
```

Avoid this pattern for menus:

```python
while True:
    console.clear()
    console.print(menu)
```

It can still leave older module outputs visible in terminal scrollback, depending on terminal behavior.

## Terminal History Cleanup

When entering or leaving submodules, clear both current screen and scrollback where supported:

```python
sys.stdout.write("\033[2J\033[3J\033[H")
sys.stdout.flush()
console.clear()
```

Use this at:

- Program startup
- Before entering a submodule
- After returning from a submodule
- Before printing long-running action handoff output
- KeyboardInterrupt cleanup

Note that scrollback clearing support depends on the terminal host.

## Long-Running Action Pattern

For long-running tasks from a TUI, prefer a non-blocking handoff:

- Spawn a detached child process.
- Redirect stdout/stderr to a log file.
- Write a PID/status file when process management is needed.
- Return control to the TUI after startup.

Keep a hidden foreground flag for external supervisors or debugging:

```bash
python -m package.main --config config.json --run-foreground
```

Expose user-facing commands when useful:

```bash
python main.py --run
python main.py --status
```

If PID management is part of the product, support:

- Reading PID from a stable file.
- Checking if the process exists.
- Cleaning stale PID files.
- Terminating the process safely.

## Configuration Flow

If a config file is missing, generate a safe local config automatically.

Recommended defaults:

- Host: `127.0.0.1`
- Port: a local default such as `8000`
- Data directory for logs, SQLite, and PID files
- Empty resource lists where appropriate
- Generated local credentials only if local authentication is needed

For secrets:

- Never commit real secrets.
- Add local config files to `.gitignore`.
- Use password prompts for secrets.
- Show generated local credentials once, then store them in local config.

## Logging and Logs Panel

Move runtime framework logs out of the interactive UI:

- Framework startup logs
- Access logs
- Runtime exceptions
- Background process stdout/stderr

Write them to a log file and expose a TUI logs panel.

If structured runtime records exist, show a separate table:

- Time
- Component or resource
- Operation or key name
- Status code
- Success/failure
- Retry flag
- Domain-specific measurements

Do not stream logs into the main menu unless explicitly requested.

## Validation Checklist

After changes, verify:

- Python files compile.
- Main edited files have no diagnostics.
- TUI arrow navigation selects expected menu items.
- Confirmation menus support up/down and Enter.
- Background or long-running action writes expected status/log files.
- Status handles running, stopped, and stale PID cases.
- Stop cleans up stale PID files.
- Config generation does not expose real secrets.
- Documentation includes both TUI and scriptable command usage.

## Communication Guidelines

When reporting results to the user:

- Explain what changed at the workflow level.
- Mention the main commands and TUI entries.
- Note platform-specific behavior and privilege requirements only when relevant.
- Link to modified files with code references.
- Mention validation performed.
