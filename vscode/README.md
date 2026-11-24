# ccanchor for VS Code

Runs [ccanchor](https://github.com/p10node/ccanchor) when a window opens, so that Claude Code's history, memory and settings have followed a moved or renamed project folder before the Claude Code extension reads its session list.

## Why

The Claude Code extension reads its session list straight from `~/.claude/projects/<encoded folder path>` and does not watch that folder. ccanchor's `SessionStart` hook runs when the Claude panel starts a session. That can be after the list was drawn: the history is back on disk, but the list stays empty until you reopen or refresh it. This extension does the same work at window startup instead.

## What it does

- At startup, and when the workspace folders change, it runs `ccanchor sync --apply`. That takes a few milliseconds when nothing moved.
- It shows a message when history followed a folder. Otherwise it stays silent.
- It uses the same `CLAUDE_CONFIG_DIR` as Claude Code, including one set in `claudeCode.environmentVariables`.

It has no background process and does not read your code.

## Requirements

The `ccanchor` binary. The extension looks for it in this order:

1. the `ccanchor.path` setting;
2. `PATH`;
3. `/opt/homebrew/bin`, `/usr/local/bin`, `~/.local/bin`;
4. `ccanchor/bin` in Claude Code's config folder (from `ccanchor install --copy`);
5. the data folder of the ccanchor Claude Code plugin;
6. the same two places in `~/.claude`, when `CLAUDE_CONFIG_DIR` points elsewhere.

See the [install instructions](https://github.com/p10node/ccanchor#install).

## Commands

| Command                              | What it does                                             |
| ------------------------------------ | -------------------------------------------------------- |
| `ccanchor: Follow Moved Folders Now` | Runs the sync and reports the result.                    |
| `ccanchor: Show Status`              | Prints `ccanchor status` to the ccanchor output channel. |

## Settings

| Setting                  | Default | What it does                                       |
| ------------------------ | ------- | -------------------------------------------------- |
| `ccanchor.path`          | empty   | Path of the binary, if it is not found on its own. |
| `ccanchor.syncOnStartup` | `true`  | Run the sync when a window opens.                  |

Both settings are machine-scoped: a workspace cannot change which binary runs.

ccanchor is not affiliated with Anthropic. MIT licensed.
