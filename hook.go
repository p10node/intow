package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type hookInput struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"`
}

// cmdHook is the SessionStart hook. It never fails: Claude must always start.
func (c *cli) cmdHook(argv []string) (int, error) {
	data, _ := io.ReadAll(io.LimitReader(c.stdin, 1<<20))
	var in hookInput
	json.Unmarshal(data, &in)
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	var reports []*Report
	var here *Report
	var warnings []string
	err := withLock(10*time.Second, func() error {
		reg := loadRegistry()
		if id, err := statIdent(cwd); err == nil && !isTemp(cwd) {
			// Is the folder Claude is starting in one that was moved here?
			if _, e := reg.find(id); e != nil && e.Path != cwd {
				if exists(e.Path) {
					warnings = append(warnings, fmt.Sprintf(
						"this folder used to be %s, which now holds another folder; run `ccanchor link %s %s` if its history belongs here",
						e.Path, shellQuote(e.Path), shellQuote(cwd)))
				} else {
					dst := ""
					if in.TranscriptPath != "" {
						dst = filepath.Dir(in.TranscriptPath)
					}
					rep, err := migrate(e.Path, cwd, migrateOpts{DstOverride: dst, ExcludeSession: in.SessionID, Reg: reg})
					if rep != nil {
						reports = append(reports, rep)
						here = rep
					}
					if err != nil {
						return err
					}
				}
			}
			reg.track(cwd)
		}
		more, err := syncAll(reg, true, in.SessionID)
		reports = append(reports, more...)
		if serr := reg.save(); err == nil {
			err = serr
		}
		return err
	})
	if err != nil {
		logError(cwd, err)
	}

	var parts []string
	for _, r := range reports {
		if r.changed() {
			parts = append(parts, r.short())
		}
	}
	parts = append(parts, warnings...)
	if len(parts) == 0 {
		return 0, nil
	}
	out := map[string]any{"systemMessage": "ccanchor: " + strings.Join(parts, "; ")}
	if here != nil && here.changed() {
		ctx := fmt.Sprintf("This project folder was moved from %s to %s. ccanchor re-attached its Claude Code history "+
			"(%d earlier session(s), including subfolders; /resume lists them again) and its auto-memory.",
			here.Old, here.New, here.Sessions)
		if idx := memoryIndex(cwd, in.TranscriptPath); idx != "" {
			ctx += "\nThe auto-memory index may not have been loaded at startup because it was still under the old path. " +
				idx + " contains:\n" + readHead(idx, 12000)
		}
		out["hookSpecificOutput"] = map[string]any{"hookEventName": "SessionStart", "additionalContext": ctx}
	}
	b, _ := encodeJSON(out, false)
	c.printf("%s\n", b)
	return 0, nil
}

func logError(cwd string, err error) {
	if os.MkdirAll(stateDir(), 0o755) != nil {
		return
	}
	f, ferr := os.OpenFile(filepath.Join(stateDir(), "error.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s hook cwd=%s: %v\n", time.Now().Format(time.RFC3339), cwd, err)
}
