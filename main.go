// Command intow keeps Claude Code chat history and memory attached to a
// folder after the folder is moved or renamed.
//
// Claude Code stores each folder's sessions and auto-memory under
// ~/.claude/projects/<encoded absolute path>/ and its per-folder settings
// under "projects" in ~/.claude.json. intow records each folder's
// filesystem identity (device, inode, birth time) from a SessionStart hook.
// When a recorded path disappears it finds where the folder went (on macOS
// through /.vol; on Linux by scanning nearby) and re-keys Claude's data.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "dev"

const usage = `intow keeps Claude Code history and memory attached to folders you move or rename.

Usage:
  intow status [--json]           folders Claude knows about and whether they moved
  intow sync [--apply] [-q]       follow tracked folders that moved (dry run by default)
  intow mv [-n] [-f] SRC DST      move a folder together with its Claude data
  intow link [-n] [-f] OLD NEW    attach the data of a path that is gone to folder NEW
  intow scan [-q]                 start tracking every existing project folder
  intow forget PATH...            stop tracking folders
  intow log [-n N]                past migrations
  intow undo [-n] [FOLDER]        take the last migration back (of FOLDER, if given)
  intow shell-init [SHELL]        a claude function that syncs first (zsh, bash, fish, powershell):
                                    eval "$(intow shell-init)"
  intow install [--bin-dir DIR]   add the SessionStart hook to ~/.claude/settings.json
  intow uninstall [--purge]       remove that hook (--purge: also all intow state)
  intow hook                      the SessionStart hook itself (run by Claude Code)
  intow version

-n is --dry-run, -f is --force, -q is --quiet.
`

var errHelp = errors.New("help")

type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

func main() {
	c := &cli{os.Stdin, os.Stdout, os.Stderr}
	os.Exit(c.run(os.Args[1:]))
}

func (c *cli) printf(format string, a ...any) { fmt.Fprintf(c.stdout, format, a...) }

func (c *cli) run(argv []string) int {
	cmd := "status"
	if len(argv) > 0 && !(strings.HasPrefix(argv[0], "-") && argv[0] != "-h" && argv[0] != "--help" && argv[0] != "--version") {
		cmd, argv = argv[0], argv[1:]
	}
	commands := map[string]func([]string) (int, error){
		"status": c.cmdStatus, "sync": c.cmdSync, "mv": c.cmdMv, "link": c.cmdLink,
		"scan": c.cmdScan, "forget": c.cmdForget, "log": c.cmdLog, "hook": c.cmdHook,
		"install": c.cmdInstall, "uninstall": c.cmdUninstall,
		"undo": c.cmdUndo, "shell-init": c.cmdShellInit,
	}
	switch cmd {
	case "version", "--version":
		c.printf("intow %s\n", version)
		return 0
	case "help", "-h", "--help":
		c.printf("%s", usage)
		return 0
	}
	fn, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(c.stderr, "intow: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	code, err := fn(argv)
	if errors.Is(err, errHelp) {
		c.printf("%s", usage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "intow: "+err.Error())
		if code == 0 {
			code = 1
		}
	}
	return code
}

type opts struct {
	flags map[string]bool
	vals  map[string]string
	args  []string
}

func (o *opts) has(names ...string) bool {
	for _, n := range names {
		if o.flags[n] {
			return true
		}
	}
	return false
}

// parseArgs accepts flags before, between or after positional arguments.
func parseArgs(in []string, bools []string, valued []string) (*opts, error) {
	isBool, isVal := map[string]bool{}, map[string]bool{}
	for _, b := range bools {
		isBool[b] = true
	}
	for _, v := range valued {
		isVal[v] = true
	}
	o := &opts{flags: map[string]bool{}, vals: map[string]string{}}
	for i := 0; i < len(in); i++ {
		a := in[i]
		if a == "--" {
			o.args = append(o.args, in[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			o.args = append(o.args, a)
			continue
		}
		name, val, hasVal := strings.TrimLeft(a, "-"), "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, hasVal = name[:j], name[j+1:], true
		}
		switch {
		case name == "h" || name == "help":
			return nil, errHelp
		case isBool[name] && !hasVal:
			o.flags[name] = true
		case isVal[name]:
			if !hasVal {
				if i+1 >= len(in) {
					return nil, fmt.Errorf("%s needs a value", a)
				}
				i++
				val = in[i]
			}
			o.vals[name] = val
		default:
			return nil, fmt.Errorf("unknown option %s", a)
		}
	}
	return o, nil
}
