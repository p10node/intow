---
name: status
description: Show which project folders intow tracks, find Claude Code sessions and memory orphaned by moving or renaming a folder, and re-attach them. Use when the user says past conversations, /resume history or memory disappeared after a folder was moved or renamed, or asks about intow.
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow status), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow status *), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow sync), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow link -n *), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow undo -n), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow undo -n *), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/intow log)
---

# intow status

Run:

```
${CLAUDE_PLUGIN_ROOT}/scripts/intow status
```

States in the output:

- `ok`: tracked and in place.
- `untracked`: exists but not tracked yet. `intow scan` fixes it, and so does starting Claude there.
- `moved`: the folder moved. intow follows it at the next session start, or immediately with `sync --apply`.
- `orphan`: the folder is gone and intow never saw where it went. This happens when it moved before intow was installed, was copied, or was cloned fresh.
- `trashed`: the folder is in the Trash. intow leaves it alone.
- `temp`: a temp directory. These are never tracked.

Then:

1. Summarize in a few lines: how many folders are fine, which moved, and which are orphans.
2. For each orphan, the output may suggest `intow link OLD NEW`. Check that NEW really is the same project before you propose it, for example the same git remote or a recognisable layout. Show the effect first:

   ```
   ${CLAUDE_PLUGIN_ROOT}/scripts/intow link -n OLD NEW
   ```

   Run it without `-n` only after the user agrees. It moves OLD's sessions and memory into NEW's history and merges OLD's per-folder settings.
3. If any folder shows `moved`, offer to run `${CLAUDE_PLUGIN_ROOT}/scripts/intow sync --apply`.
4. The output may end with a `intow link --force OLD NEW` line: the folder was moved aside and another folder now uses its old path. `--force` moves every session recorded under OLD, including any the other folder has made since. Explain that, show `link -n --force OLD NEW`, and leave the decision to the user.
5. If the output says automatic following is paused, Claude Code changed how it names project folders. Tell the user to update intow; do not move anything by hand.

If the user says intow moved history that should have stayed, `${CLAUDE_PLUGIN_ROOT}/scripts/intow log` lists the migrations and `intow undo -n [FOLDER]` shows what taking the last one back would do. It returns what the migration moved; sessions continued in the new place since stay there. Run `undo` without `-n` only after the user agrees.

Never delete or edit anything under `~/.claude` yourself; intow keeps backups of the files it changes in `~/.claude/intow/backups/`.
