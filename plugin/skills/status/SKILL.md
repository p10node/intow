---
name: status
description: Show which project folders ccanchor tracks, find Claude Code sessions and memory orphaned by moving or renaming a folder, and re-attach them. Use when the user says past conversations, /resume history or memory disappeared after a folder was moved or renamed, or asks about ccanchor.
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor status), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor status *), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor sync), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor link -n *), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor undo -n), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor undo -n *), Bash(${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor log)
---

# ccanchor status

Run:

```
${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor status
```

States in the output:

- `ok`: tracked and in place.
- `untracked`: exists but not tracked yet. `ccanchor scan` fixes it, and so does starting Claude there.
- `moved`: the folder moved. ccanchor follows it at the next session start, or immediately with `sync --apply`.
- `orphan`: the folder is gone and ccanchor never saw where it went. This happens when it moved before ccanchor was installed, was copied, or was cloned fresh.
- `trashed`: the folder is in the Trash. ccanchor leaves it alone.
- `temp`: a temp directory. These are never tracked.

Then:

1. Summarize in a few lines: how many folders are fine, which moved, and which are orphans.
2. For each orphan, the output may suggest `ccanchor link OLD NEW`. Check that NEW really is the same project before you propose it, for example the same git remote or a recognisable layout. Show the effect first:

   ```
   ${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor link -n OLD NEW
   ```

   Run it without `-n` only after the user agrees. It moves OLD's sessions and memory into NEW's history and merges OLD's per-folder settings.
3. If any folder shows `moved`, offer to run `${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor sync --apply`.
4. The output may end with a `ccanchor link --force OLD NEW` line: the folder was moved aside and another folder now uses its old path. `--force` moves every session recorded under OLD, including any the other folder has made since. Explain that, show `link -n --force OLD NEW`, and leave the decision to the user.
5. If the output says automatic following is paused, Claude Code changed how it names project folders. Tell the user to update ccanchor; do not move anything by hand.

If the user says ccanchor moved history that should have stayed, `${CLAUDE_PLUGIN_ROOT}/scripts/ccanchor log` lists the migrations and `ccanchor undo -n [FOLDER]` shows what taking the last one back would do. It returns what the migration moved; sessions continued in the new place since stay there. Run `undo` without `-n` only after the user agrees.

Never delete or edit anything under `~/.claude` yourself; ccanchor keeps backups of the files it changes in `~/.claude/ccanchor/backups/`.
