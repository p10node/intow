<img src="plugin/.claude-plugin/icon.png" width="96" align="right" alt="">

# intow

intow keeps Claude Code chat history, memory and per-folder settings attached to a folder after you move or rename it. You can rename the folder in Finder, in your editor or with `mv`. The next Claude session in the new place has its past sessions in `/resume`, its auto-memory and its permissions back. You don't need to remember a special command.

The name: the folder moves, and its history comes along in tow.

## The problem

Claude Code stores everything about a folder under the folder's absolute path:

| What                               | Where                                       |
| ---------------------------------- | ------------------------------------------- |
| Sessions (`/resume`, `--continue`) | `~/.claude/projects/<encoded path>/*.jsonl` |
| Auto-memory                        | `~/.claude/projects/<encoded path>/memory/` |
| Trust, allowed tools, MCP choices  | `"projects"."<path>"` in `~/.claude.json`   |

If you rename `~/code/app` to `~/code/app-v2`, Claude starts from nothing there. The old data stays behind under a name nothing points to any more.

Other tools can carry the data across, for example [claude-mv](https://github.com/justinstimatze/claude-mv), [ccmv](https://github.com/seflue/ccmv), [claude-repath](https://pypi.org/project/claude-repath/), [mvcp](https://github.com/plosson/mvcp) and [claude-code-project-mover](https://github.com/skydiver/claude-code-project-mover). They do the job well when you remember to use them instead of `mv`. intow is for the times you don't: it notices the move on its own.

## Install

**Claude Code plugin** (recommended):

```
/plugin marketplace add p10node/intow
/plugin install intow@intow
```

The first time the plugin runs, it downloads the intow binary for your platform (about 1.6 MB) from the GitHub release that matches the plugin version. The plugin carries the SHA-256 of that binary in its own `checksums.txt` and runs nothing else, so a replaced release asset is rejected. `/intow:status` lists the folders intow knows about and helps you re-attach history that moved before you installed it.

**Homebrew:**

```sh
brew install p10node/tap/intow
intow install        # adds the SessionStart hook to ~/.claude/settings.json
```

On **Windows**, use the plugin, or take `intow.exe` from the [releases](https://github.com/p10node/intow/releases) and run `intow install`.

**Script** (macOS and Linux; installs to `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/p10node/intow/main/scripts/install.sh | sh
intow install
```

Use the plugin or `intow install`, not both. If both are present the second run finds nothing to do, so nothing breaks.

**Shell function** (optional, for `claude --continue` and `claude --resume`):

```sh
eval "$(intow shell-init)"        # zsh, bash; put it in ~/.zshrc or ~/.bashrc
intow shell-init fish | source    # fish
intow shell-init powershell | Out-String | Invoke-Expression   # PowerShell; put it in $PROFILE
```

Claude Code looks for the session to continue before it runs any hook. Right after a rename, `claude --continue` therefore ends with "No conversation found to continue" and asks you to trust the folder again, and the hook never gets its turn. The function runs `intow sync --apply -q` first, which takes a few milliseconds, and then starts Claude. With only the plugin installed, `intow` is not on your `PATH`: run `/intow:shell` in Claude Code, which prints the same function with the plugin binary's path.

**VS Code extension** (optional, for the Claude Code extension's session list): [`vscode/`](vscode) in this repository.

The Claude Code extension reads its session list from disk when a window opens and does not watch the folder. The hook runs a moment later, when the Claude panel starts its session, so a list that is already on screen stays empty until it is reopened. The extension runs `intow sync --apply` at window startup instead, before anything reads the list. It needs the `intow` binary from one of the installs above.

## How it works

1. **Record.** When a Claude session starts, a `SessionStart` hook records the folder's filesystem identity: its device, inode and birth time. A directory keeps this identity when it is renamed or moved within a disk.
2. **Find.** Later, if a recorded path no longer exists, intow looks for the same identity:
   - On **macOS** it asks the kernel directly, through `/.vol/<device>/<inode>` and `fcntl(F_GETPATH)`. This is exact.
   - On **Linux** it searches near the old location for a directory with the same inode and birth time.
   - On **Windows** it asks the volume for the folder's file ID (`OpenFileById`). This is exact on NTFS and ReFS.
3. **Re-key.**
   - It renames `~/.claude/projects/<old>` to `<new>`, or merges the two if Claude already created `<new>`. Memory indexes are merged line by line.
   - It rewrites the `cwd` field in the session files and keeps their modification times, so `/resume` keeps its order.
   - It moves the folder's entry in `~/.claude.json`, keeping the key order. Lists are combined, and trust stays granted. The folders listed under `githubRepoPaths` are updated too.
   - It updates `history.jsonl` and the folder recorded for project-scope plugins in `plugins/installed_plugins.json`.
   - Subfolders move along with the folder.
4. **Tell Claude.** If the session that is starting is in the moved folder, the hook also passes the folder's `MEMORY.md` to it.

intow runs only when Claude starts (or just before, with the shell function), and it adds a few milliseconds. It has no background process, adds nothing to Login Items and asks for no new privacy permission, because it runs as part of Claude Code.

On macOS a rename that only changes the case of the name (`app` to `App`) is followed too. The old name still opens the folder there, but Claude Code keys its data by the exact spelling.

It deliberately leaves a folder alone in these cases:

- The folder is in the Trash.
- Claude is still running in the old path. intow retries at the next start.
- The old path now holds a different folder, for example after `mv app app-old` and a fresh clone into `app`. The history under that path could belong to either, so it stays. intow remembers the folder that was moved aside, and the hook and `status` print the `intow link --force` command that hands the history to it.
- Claude Code names project folders differently than intow expects, because a new version changed the scheme. intow compares the two at every session start and pauses itself instead of moving data to a place Claude does not read. A session started with `CLAUDE_CODE_PROJECT_DIR_NAME` is left alone as well: its data does not depend on the path.

## Commands

| Command                                 | What it does                                                                                                                                                                                                                                                                                                           |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `intow status`                          | Lists the folders Claude knows about: `ok`, `moved`, `orphan`, `trashed`, `untracked`, `temp`. For orphans it suggests a `link` command.                                                                                                                                                                               |
| `intow sync [--apply]`                  | Follows every tracked folder that moved. Without `--apply` it only shows what it would do.                                                                                                                                                                                                                             |
| `intow mv SRC DST`                      | Moves a folder and its Claude data together. Works across disks.                                                                                                                                                                                                                                                       |
| `intow link OLD NEW`                    | Attaches the data of a path that no longer exists to the folder `NEW`. Use it for moves made before intow was installed. With `--force` it also takes the data of a path that another folder uses now, all of it.                                                                                                      |
| `intow scan`                            | Starts tracking every existing project folder. `install` runs this once.                                                                                                                                                                                                                                               |
| `intow forget PATH…`                    | Stops tracking folders.                                                                                                                                                                                                                                                                                                |
| `intow log`                             | Lists past migrations.                                                                                                                                                                                                                                                                                                 |
| `intow undo [FOLDER]`                   | Takes the last migration back, or the last one of `FOLDER`. What it moved returns under the old path. Sessions you have continued in the new place since, and settings Claude has changed there, stay where they are. The folder is no longer tracked afterwards. Run it again to take back the migration before that. |
| `intow shell-init [SHELL]`              | Prints the `claude` shell function described under Install (`zsh`, `bash`, `fish`, `powershell`).                                                                                                                                                                                                                      |
| `intow install` / `uninstall [--purge]` | Adds or removes the hook in `~/.claude/settings.json`. `--purge` also deletes intow's data.                                                                                                                                                                                                                            |

`-n` (`--dry-run`) works with `mv`, `link` and `undo`.

## Safety

- Before changing `~/.claude.json`, `settings.json` or `installed_plugins.json`, intow copies them to `~/.claude/intow/backups/` and keeps the last 20 copies.
- Claude Code rewrites `~/.claude.json` while it runs. intow edits it under the lock Claude itself uses (`~/.claude.json.lock`), re-reads the file right before replacing it and starts over if it changed.
- Files that are symlinks stay symlinks. If `settings.json`, `history.jsonl` or `~/.claude.json` point into a dotfiles folder (stow, chezmoi, home-manager), intow updates the file they point at.
- Each migration is logged to `~/.claude/intow/log.jsonl`, and every step of it is recorded in `~/.claude/intow/journal/` (the last 50). `intow undo` reverses exactly those steps.
- Two names that lead to one directory, as on a case-insensitive volume, are never merged into each other.
- intow never deletes a session file. If a file name clashes, it leaves both files in place and reports the clash.
- The text of past messages is not changed. Absolute paths inside old messages and tool calls stay as they were.
- Release assets carry build provenance: `gh attestation verify intow_<version>_<os>_<arch>.tar.gz --repo p10node/intow`.

## Platform support

|         | Follows moves automatically                                                                                               | `mv`, `link`        |
| ------- | ------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| macOS   | yes (exact, via `/.vol`)                                                                                                  | yes                 |
| Linux   | yes, when the filesystem records birth times (ext4, btrfs, xfs) and the folder moved within a few levels of its old place | yes                 |
| Windows | yes, on NTFS and ReFS (file IDs); not on FAT or exFAT                                                                     | yes, within a drive |

## Limits

- **Moves made before intow was installed.** intow only knows a folder's identity after it has seen the folder once. `status` lists earlier moves as orphans and suggests a `link` command.
- **Copies.** Copying to another disk or cloning a repository afresh creates a new folder with a new identity. Use `intow mv` for the move, or `link` afterwards.
- **`claude --continue` and `claude --resume` right after a move.** Claude Code looks for the session before it runs hooks, so the hook comes too late for these two. Install the shell function (see Install), or run `intow sync --apply` once.
- **Editors.** The Claude Code extension for VS Code starts Claude, and with it the hook, when you open the Claude panel. Its session list is read before that and is not refreshed on its own: reopen it, or install the VS Code extension from this repository (see Install).
- **Windows is new.** Its file-ID code has been tested under Wine, not yet on many real machines. A folder that programs spell in two ways (`C:\` in a terminal, `c:\` from an editor) keeps one history per spelling, as Claude Code itself does; intow moves both. A rename that only changes case is not followed there, and neither is a move to another drive: use `intow link`.
- **Linux, hidden folders.** The search skips hidden and dependency folders (`.cache`, `node_modules`), except the ones the project lived in.
- **State intow cannot reach.** What an editor stores per workspace (open panels, session groups), and absolute paths written inside the project itself, for example in `.claude/settings.local.json`.

## Development

```sh
go test ./...
scripts/e2e.sh          # against a real, logged-in Claude Code; uses three small Haiku requests
scripts/wine-test.sh    # the test suite of the Windows build, under Wine in a container
(cd vscode && node --test)   # the VS Code extension
vscode/e2e/docker.sh    # the VS Code extension inside a real VS Code, in a container
```

To release, tag a commit `vX.Y.Z` and push the tag. CI then:

1. tests the code, builds the binaries with GoReleaser and publishes the release;
2. attests the build provenance of every asset;
3. commits the release's checksums to `plugin/checksums.txt` together with the new version in `plugin/.claude-plugin/plugin.json` (`scripts/pin-plugin.sh`), so plugin users only move to a version whose binaries exist and are pinned;
4. updates `p10node/homebrew-tap`, if the `HOMEBREW_TAP_TOKEN` secret is set.

Step 3 pushes to `main`, so the workflow's token must be allowed to.

intow is not affiliated with Anthropic. MIT licensed.
