<img src="plugin/.claude-plugin/icon.png" width="96" align="right" alt="">

# ccanchor

ccanchor keeps Claude Code chat history, memory and per-folder settings attached to a folder after you move or rename it. You can rename the folder in Finder, in your editor or with `mv`. The next Claude session in the new place has its past sessions in `/resume`, its auto-memory and its permissions back. You don't need to remember a special command.

## The problem

Claude Code stores everything about a folder under the folder's absolute path:

| What | Where |
|---|---|
| Sessions (`/resume`, `--continue`) | `~/.claude/projects/<encoded path>/*.jsonl` |
| Auto-memory | `~/.claude/projects/<encoded path>/memory/` |
| Trust, allowed tools, MCP choices | `"projects"."<path>"` in `~/.claude.json` |

If you rename `~/code/app` to `~/code/app-v2`, Claude starts from nothing there. The old data stays behind under a name nothing points to any more.

Other tools can carry the data across, for example [claude-mv](https://github.com/justinstimatze/claude-mv), [ccmv](https://github.com/seflue/ccmv), [claude-repath](https://pypi.org/project/claude-repath/), [mvcp](https://github.com/plosson/mvcp) and [claude-code-project-mover](https://github.com/skydiver/claude-code-project-mover). They do the job well when you remember to use them instead of `mv`. ccanchor is for the times you don't: it notices the move on its own.

## Install

**Claude Code plugin** (recommended):

```
/plugin marketplace add p10node/ccanchor
/plugin install ccanchor@ccanchor
```

The first time the plugin runs, it downloads the ccanchor binary for your platform (about 1.6 MB). The binary comes from the GitHub release that matches the plugin version, and the plugin checks it against that release's SHA-256 checksums. `/ccanchor:status` lists the folders ccanchor knows about and helps you re-attach history that moved before you installed it.

**Homebrew:**

```sh
brew install p10node/tap/ccanchor
ccanchor install        # adds the SessionStart hook to ~/.claude/settings.json
```

**Script** (macOS and Linux; installs to `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/p10node/ccanchor/main/scripts/install.sh | sh
ccanchor install
```

Use the plugin or `ccanchor install`, not both. If both are present the second run finds nothing to do, so nothing breaks.

## How it works

1. **Record.** When a Claude session starts, a `SessionStart` hook records the folder's filesystem identity: its device, inode and birth time. A directory keeps this identity when it is renamed or moved within a disk.
2. **Find.** Later, if a recorded path no longer exists, ccanchor looks for the same identity:
   - On **macOS** it asks the kernel directly, through `/.vol/<device>/<inode>` and `fcntl(F_GETPATH)`. This is exact.
   - On **Linux** it searches near the old location for a directory with the same inode and birth time.
3. **Re-key.**
   - It renames `~/.claude/projects/<old>` to `<new>`, or merges the two if Claude already created `<new>`. Memory indexes are merged line by line.
   - It rewrites the `cwd` field in the session files and keeps their modification times, so `/resume` keeps its order.
   - It moves the folder's entry in `~/.claude.json`, keeping the key order. Lists are combined, and trust stays granted.
   - It updates `history.jsonl`.
   - Subfolders move along with the folder.
4. **Tell Claude.** If the session that is starting is in the moved folder, the hook also passes the folder's `MEMORY.md` to it.

ccanchor runs only when Claude starts, and it adds a few milliseconds. It has no background process, adds nothing to Login Items and asks for no new privacy permission, because it runs as part of Claude Code.

It deliberately leaves a folder alone in these cases:

- The folder is in the Trash.
- Claude is still running in the old path. ccanchor retries at the next start.
- The old path now holds a different folder. ccanchor prints the `ccanchor link` command to run if the history belongs to the moved folder after all.

## Commands

| Command | What it does |
|---|---|
| `ccanchor status` | Lists the folders Claude knows about: `ok`, `moved`, `orphan`, `trashed`, `untracked`, `temp`. For orphans it suggests a `link` command. |
| `ccanchor sync [--apply]` | Follows every tracked folder that moved. Without `--apply` it only shows what it would do. |
| `ccanchor mv SRC DST` | Moves a folder and its Claude data together. Works across disks. |
| `ccanchor link OLD NEW` | Attaches the data of a path that no longer exists to the folder `NEW`. Use it for moves made before ccanchor was installed. |
| `ccanchor scan` | Starts tracking every existing project folder. `install` runs this once. |
| `ccanchor forget PATH…` | Stops tracking folders. |
| `ccanchor log` | Lists past migrations. |
| `ccanchor install` / `uninstall [--purge]` | Adds or removes the hook in `~/.claude/settings.json`. `--purge` also deletes ccanchor's data. |

`-n` (`--dry-run`) works with `mv` and `link`.

## Safety

- Before changing `~/.claude.json` or `settings.json`, ccanchor copies them to `~/.claude/ccanchor/backups/` and keeps the last 20 copies.
- Each migration is logged to `~/.claude/ccanchor/log.jsonl`.
- ccanchor never deletes a session file. If a file name clashes, it leaves both files in place and reports the clash.
- The text of past messages is not changed. Absolute paths inside old messages and tool calls stay as they were.

## Platform support

| | Follows moves automatically | `mv`, `link` |
|---|---|---|
| macOS | yes (exact, via `/.vol`) | yes |
| Linux | yes, when the filesystem records birth times (ext4, btrfs, xfs) and the folder moved within a few levels of its old place | yes |
| Windows | planned (NTFS file IDs) | planned |

## Limits

- **Moves made before ccanchor was installed.** ccanchor only knows a folder's identity after it has seen the folder once. `status` lists earlier moves as orphans and suggests a `link` command.
- **Copies.** Copying to another disk or cloning a repository afresh creates a new folder with a new identity. Use `ccanchor mv` for the move, or `link` afterwards.
- **The `/resume` picker right after a move.** The picker appears before session hooks run. If the very first thing you do after a move is open `/resume`, start any session first, or run `ccanchor sync --apply`.

## Development

```sh
go test ./...
scripts/e2e.sh   # against a real, logged-in Claude Code; uses three small Haiku requests
```

To release:

1. Set the new version in `plugin/.claude-plugin/plugin.json`.
2. Tag the commit `vX.Y.Z` and push the tag.

CI then tests the code, builds the binaries with GoReleaser and publishes the release. If the `HOMEBREW_TAP_TOKEN` secret is set, it also updates `p10node/homebrew-tap`.

ccanchor is not affiliated with Anthropic. MIT licensed.
