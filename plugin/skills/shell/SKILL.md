---
name: shell
description: Set up ccanchor's shell function so that `claude --continue` and `claude --resume` find their session right after a folder was moved or renamed. Use when the user reports "No conversation found to continue" after renaming or moving a folder, or asks for ccanchor's shell integration.
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor shell-init), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor shell-init *)
---

# ccanchor shell function

Claude Code looks for the session to continue before it runs any hook. Right after a rename, `claude --continue` and `claude --resume` therefore fail, and ccanchor's hook never runs. A small `claude` shell function fixes this: it lets ccanchor follow moved folders first, then starts Claude.

Print the function for the user's shell (`zsh`, `bash` or `fish`; without an argument it uses `$SHELL`):

```
${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor shell-init
```

Then:

1. Show the output and say which file it belongs in: `~/.zshrc`, `~/.bashrc`, or `~/.config/fish/functions/claude.fish`.
2. Add it to that file only if the user asks you to. Check first that the file does not already define a `claude` function or alias, and say so if it does.
3. Tell the user to open a new terminal, or to source the file, for it to take effect.

The function calls the binary by a path that stays the same across plugin updates. If the binary is missing, the function skips it and starts Claude as usual.
