# RFC 0001: Follow the folder, not the path

Project identity for the state that coding agents keep about a folder.

| Field                    | Value                                        |
| ------------------------ | -------------------------------------------- |
| Status                   | Draft 0.1                                    |
| Date                     | 2026-11-24                                   |
| Reference implementation | [intow](https://github.com/p10node/intow)    |
| Discussion               | https://github.com/p10node/intow/discussions |

## Summary

Coding agents file what they know about a project under the absolute path of its folder: past sessions, memory, trust and permission decisions, prompt history. When the folder is renamed or moved, all of it stays behind under the old path and the agent starts from nothing.

This RFC proposes that an agent identify a project by the folder itself and treat the path as an attribute that can change. It defines a folder identity that the operating system already maintains, so nothing has to be written into the project. It then gives the lookup rules and the safety rules for carrying existing state across. All of it comes from a working implementation for Claude Code.

There are two audiences. Vendors can apply the lookup natively, which is a small change. Authors of third-party tools can apply the safety rules today.

## The problem

The same report exists for every agent we looked at.

| Agent       | What a rename breaks                                          | Reports                                                         | Vendor response in those threads                                                             |
| ----------- | ------------------------------------------------------------- | --------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| Claude Code | Sessions, memory, trust, allowed tools, prompt history        | 14 closed and locked (2025-11 to 2026-08), 5 open, listed below | None in the three threads we read in full                                                    |
| Codex       | Threads keep the old working directory; shell and writes fail | openai/codex #26836 (open), #41776 (open), #11019 (closed)      | None in #26836                                                                               |
| OpenCode    | Sessions of the old path are not listed                       | anomalyco/opencode #29703, closed by an inactivity bot          | None                                                                                         |
| Cursor      | Chat history of the old location is not shown                 | Several forum threads; the reply quoted is from 2025-08         | "this isn't a bug, each chat history is strictly tied to the name/location of your projects" |

For Claude Code 2.1.323 to 2.1.330 we checked what a user sees after `mv app app-v2`:

- `/resume` and the editor's session list are empty.
- `claude --continue` prints "No conversation found to continue" and exits. With `-p` it starts a new session without saying so.
- The folder has to be trusted again. Allowed tools and MCP choices are gone.
- Auto-memory is empty.

Nothing has been deleted at that point. The data is under a name that nothing points to. Claude Code removes transcripts older than `cleanupPeriodDays` (30 by default), so the orphaned sessions do disappear within a month.

Open Claude Code issues about path-keyed state: #5768, #86713, #91735, #96013, #96018, #103094. Closed and locked: #12680, #24789, #27473, #33634, #33636, #37954, #41630, #47584, #48394, #52494, #69752, #70470, #79215, #84918.

## Goals

- A rename or move within a volume keeps a project's state attached, with no action from the user.
- Nothing is written into the project folder.
- A copy of a folder is a different project.
- A tool that cannot be sure does nothing and says so.
- It works without the vendor changing anything, and gets simpler when the vendor does.

Not in scope: a shared format for sessions or memory, moving state into the project, and syncing state between machines.

## Why not an ID file in the project

Several earlier proposals put a generated ID in the project: `.claude/workspace.json` (a comment on claude-code #48394), `.opencode/project.json` (opencode #29703), `.claude/project-id` (claude-code #41630). The most complete one is claude-code #103094, which is open: a random UUID in an unversioned `.claude-id` marker, with everything else kept outside the project. An ID file is the only signal that survives a move to another machine, and this RFC keeps room for it. As the primary key it has costs:

- It needs write access to the project and adds a file per tool, or an agreement on one name. agentsmd/agents.md #71 has discussed a shared directory since 2025-09 without a decision.
- A committed ID is shared by every clone, so two checkouts on one machine collide. An ignored ID is lost on a fresh clone.
- `cp -r` duplicates the ID. Detecting that needs a second signal that tells the two folders apart, which is the identity this RFC starts from.
- An ID that arrives with a repository is input from a stranger. If trust or permissions are keyed by it, a cloned repository can claim the ID of a project the user already trusts. #103094 sees this and asks for a trust record outside the project, the directory's last known path and filesystem identity, before a marker is believed. That record is the identity this RFC starts from.
- Nobody benefits until a vendor ships it.

## Proposal

### 1. Folder identity

The identity of a folder is what the filesystem keeps constant when the folder is renamed or moved within a volume.

| Platform     | Identity                                  | Notes                                                                                     |
| ------------ | ----------------------------------------- | ----------------------------------------------------------------------------------------- |
| macOS, Linux | inode number + birth time in nanoseconds  | The device number is recorded but not compared: it can change across reboots and remounts |
| Windows      | file ID (64-bit on NTFS, 128-bit on ReFS) | Not available on FAT and exFAT                                                            |

The birth time guards against inode reuse. Where the filesystem does not record one, the identity is not safe to act on and the tool falls back to the path.

### 2. Registry

Each tool keeps a small per-user registry in its own state directory. One entry per folder it has been started in:

```json
{ "path": "/home/u/code/app", "dev": 66306, "ino": 4823040, "birth_ns": 1762472304923668596, "seen": 1795513549 }
```

The registry is updated whenever the tool starts in a folder. It holds paths and filesystem numbers only.

### 3. Lookup

When the tool starts in a folder at path `P`:

1. Read the folder's identity and look it up in the registry.
2. Found with the same path: nothing to do.
3. Found with another path `P_old`: the folder moved from `P_old` to `P`. Carry its state over (section 4), then update the entry.
4. Not found: record it as a new folder.

Some state is read before the tool runs in the folder: an editor's session list, or the lookup behind `--continue`. For that, a second entry point walks the registry and asks, for each entry whose path no longer leads to that identity, where the folder is now:

| Platform | How                                                            | Cost                             |
| -------- | -------------------------------------------------------------- | -------------------------------- |
| macOS    | `/.vol/<device>/<inode>` and `fcntl(F_GETPATH)`                | One call, exact                  |
| Windows  | `OpenFileById` and `GetFinalPathNameByHandle`                  | One call, exact                  |
| Linux    | Search near the old location for the same inode and birth time | Bounded directory walk, can miss |

Linux has no unprivileged way to go from an inode to a path. This is the weakest part of the design; see the open questions.

### 4. Rules for carrying state

These apply to any tool that re-keys an agent's state, automatic or manual. Each one comes from a bug we hit or a hazard reported in the issues above.

| #   | Rule                                                                                                 | What goes wrong without it                                                                                                   |
| --- | ---------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| R1  | If the old path now holds a different folder, do nothing automatically                               | After `mv app app-old` and a fresh clone into `app`, the history could belong to either                                      |
| R2  | Before merging two state directories, check that they are not the same directory                     | On a case-insensitive volume the directories for `app` and `App` are one. Merging and removing the source deletes everything |
| R3  | Match paths on boundaries; rewrite only structured fields                                            | Replacing `/p/app` as a substring also rewrites `/p/app-docker`. Text of old messages is not ours to change                  |
| R4  | When the destination already has state, merge and keep both sides of a conflict                      | The agent may have created the new entry before the tool ran                                                                 |
| R5  | Edit shared files under the owner's lock, re-read before replacing                                   | The agent rewrites its config while it runs; a stale copy written back loses its changes                                     |
| R6  | Write through symlinks                                                                               | Dotfile managers and profile switchers link these files; replacing a link with a file breaks them silently                   |
| R7  | Journal every step and offer undo; undo must not overwrite what changed since                        | A wrong guess is otherwise permanent                                                                                         |
| R8  | Compare the agent's own name for its state directory with the one you compute; stop when they differ | A new version changed the naming scheme, and data would be moved to a place the agent does not read                          |
| R9  | Ignore temporary folders; a folder in the trash is trashed, not moved                                | State follows a deleted project into the trash                                                                               |
| R10 | Carry the state of subfolders along                                                                  | Sessions started in `app/server` are filed under their own path                                                              |

For Claude Code, R5 means `~/.claude.json`. In the versions above we observed it being written under a directory lock at `~/.claude.json.lock`, through a temporary file and a rename. A tool that takes the same lock and re-reads before replacing did not lose edits in our tests while Claude Code was starting and running hooks. We have not examined every code path that writes the file.

### 5. What a vendor would do natively

Most of section 4 exists only because a third party has to rewrite keys. A vendor does not have to:

1. Key stored state by an opaque project ID, not by the path.
2. Keep an index from folder identity to project ID, and from project ID to last known path.
3. At startup resolve by identity first, then by path for state written by older versions, then create a new ID.

A rename then costs one index update. No transcript is rewritten, and rules R3 to R8 have nothing to apply to.

Smaller steps that help on their own:

- When the agent starts in a folder that has no sessions, but whose identity is known under another path, say so. Do not silently start fresh.
- Run a hook before the session lookup of `--continue` and `--resume`. In Claude Code the `SessionStart` hook runs after that lookup, so no hook can repair it.
- Let editor extensions re-read the session list when the project's session directory changes.
- Provide `project mv` and `project link` commands, and list orphaned state in the diagnostics command.

### 6. Optional portable ID

Folder identity ends at the volume boundary. For moves to another disk or machine, a tool may read an explicit ID from the project as a hint. Two constraints:

- It is a hint for offering a link. It never moves trust or permissions by itself.
- It is read only when the folder's own identity is unknown.

Where that ID lives is left open on purpose.

## Cases

| Case                                             | Result                                                                                                 |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------------ |
| Rename, or move within a volume                  | Same identity. State follows                                                                           |
| Rename that only changes case                    | Followed on macOS. Not on Windows, where programs spell one path in several ways                       |
| `cp -r`, fresh `git clone`                       | New identity. New project, no shared ID to untangle                                                    |
| Move to another volume                           | New identity. Use a move command that carries the state, or link afterwards                            |
| Restore from backup, new machine                 | New identity. Link by hand, or use the portable ID as a hint                                           |
| Folder deleted, another created at the same path | Different identity at a known path. State stays with the path unless the old folder is found elsewhere |
| Folder moved aside, another put in its place     | R1. The tool remembers the folder that was moved aside and offers to link it                           |
| Git worktree                                     | Its own folder, its own identity                                                                       |
| Path reached through a symlink                   | Resolve to the real path first                                                                         |
| Network or FUSE filesystem                       | Inode numbers may not be stable. Open question                                                         |

## Security and privacy

- Nothing in the project takes part in identification, so a repository cannot influence which state it is attached to.
- By construction, a trust decision keyed by path applies to whatever folder is later placed at that path. Keyed by identity, it stays with the folder the user approved.
- The registry is local, readable by the user only, and contains paths and filesystem numbers.

## Reference implementation

intow is a Go command-line tool with a Claude Code plugin and a VS Code extension. It implements sections 1 to 4.

| Entry point          | Covers                                                            |
| -------------------- | ----------------------------------------------------------------- |
| `SessionStart` hook  | Normal starts, and editors (the hook runs when the panel opens)   |
| Shell function       | `claude --continue` and `--resume`, by running before Claude Code |
| Editor extension     | The editor's session list, by running when a window opens         |
| `mv`, `link`, `undo` | Moves across volumes, moves made before installation, mistakes    |

What it re-keys for Claude Code: the project directory under `projects/`, the `cwd` field of transcripts, the `projects` entries and `githubRepoPaths` in `~/.claude.json`, the `project` field in `history.jsonl`, and `projectPath` in `plugins/installed_plugins.json`.

Tested on macOS and Linux against the Claude Code versions above. The test suite also passes on macOS, Linux and Windows runners in CI. The Windows build has not been run against Claude Code on a real Windows machine.

Known limits:

- A folder has to be seen once before it can be followed. Earlier moves are linked by hand.
- On Linux the search covers a few levels around the old location and needs a filesystem that records birth times (ext4, btrfs, xfs).
- State an editor keeps per workspace is out of reach.
- `jobs/` and built-in profiles of Claude Code, listed in claude-code #96018, are not handled yet.

## Open questions

1. Linux: is there a better unprivileged way from an inode to a path than a bounded search?
2. Which filesystems report unstable inode numbers or no birth time, and how should a tool detect them?
3. Should several agents share one registry, for example under `$XDG_STATE_HOME`, so each folder is tracked once?
4. Where should the optional portable ID live, and who may write it?
5. Should a worktree share state with its main checkout? Per its documentation, Claude Code's memory is keyed by the repository root while its sessions are keyed by the working directory.
6. In a monorepo, is the project the folder the agent starts in, or the repository root?

## Prior work

- Stable ID in the project: claude-code #103094 (open; an unversioned marker plus a trust record outside the project), #48394 (comment of 2026-07-03), #41630, #79215; opencode #29703; codex #21996.
- A move command: claude-code #96018, which also has the most complete inventory of what holds the path; codex #26836.
- State in the project: claude-code #96013.
- Tools that re-key on request: the `claude-mv` family, `claude-move`, `claude-migrate`, `claude-rehome`, `ccmv`. For Codex, Cursor and OpenCode: ChatAnchor, which matches moved folders by git remotes, commits and earlier paths.

## How to respond

- Tool authors: tell us which rules in section 4 you disagree with, and which cases are missing. Test cases are welcome.
- Users: describe moves that were not followed, with the platform and filesystem.
- Vendors: the four smaller steps in section 5 are independent of each other. Any one of them helps.

Open a discussion at https://github.com/p10node/intow/discussions, or send a pull request against this file.
