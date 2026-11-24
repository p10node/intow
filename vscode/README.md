# intow for VS Code

Runs [intow](https://github.com/p10node/intow) when a window opens, so that Claude Code's history, memory and settings have followed a moved or renamed project folder before the Claude Code extension reads its session list or asks about trust.

## Why

The Claude Code extension reads its session lists straight from `~/.claude/projects/<encoded folder path>` and does not watch that folder. intow's `SessionStart` hook runs when Claude Code starts in the window, which is when you open its panel or its Session Manager. For two things that is a moment too late (seen with version 2.1.330 of the Claude Code extension):

- The Session Manager in the side bar has already read its list. For a renamed folder it says "No sessions yet", and it keeps saying so until the window is reloaded, although the history is back on disk.
- If you open the panel first, it asks whether you trust the folder, although the trust you gave follows a few seconds later.

The history popup in the panel reads the list every time it opens, so it is not affected. This extension does the hook's work at window startup instead, before either of these.

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
