# intow for VS Code

Runs [intow](https://github.com/p10node/intow) when a window opens, so that Claude Code's history, memory and settings have followed a moved or renamed project folder before the Claude Code extension reads its session list.

## Why

The Claude Code extension reads its session list straight from `~/.claude/projects/<encoded folder path>` and does not watch that folder. intow's `SessionStart` hook runs when the Claude panel starts a session. That can be after the list was drawn: the history is back on disk, but the list stays empty until you reopen or refresh it. This extension does the same work at window startup instead.

## What it does

- At startup, and when the workspace folders change, it runs `intow sync --apply`. That takes a few milliseconds when nothing moved.
- It shows a message when history followed a folder. Otherwise it stays silent.
- It uses the same `CLAUDE_CONFIG_DIR` as Claude Code, including one set in `claudeCode.environmentVariables`.

It has no background process and does not read your code.

## Requirements

The `intow` binary. The extension looks for it in this order:

1. the `intow.path` setting;
2. `PATH`;
3. `/opt/homebrew/bin`, `/usr/local/bin`, `~/.local/bin`;
4. `intow/bin` in Claude Code's config folder (from `intow install --copy`);
5. the data folder of the intow Claude Code plugin;
6. the same two places in `~/.claude`, when `CLAUDE_CONFIG_DIR` points elsewhere.

See the [install instructions](https://github.com/p10node/intow#install).

## Commands

| Command                           | What it does                                       |
| --------------------------------- | -------------------------------------------------- |
| `intow: Follow Moved Folders Now` | Runs the sync and reports the result.              |
| `intow: Show Status`              | Prints `intow status` to the intow output channel. |

## Settings

| Setting               | Default | What it does                                       |
| --------------------- | ------- | -------------------------------------------------- |
| `intow.path`          | empty   | Path of the binary, if it is not found on its own. |
| `intow.syncOnStartup` | `true`  | Run the sync when a window opens.                  |

Both settings are machine-scoped: a workspace cannot change which binary runs.

intow is not affiliated with Anthropic. MIT licensed.
