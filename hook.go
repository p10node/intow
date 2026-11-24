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

type naming int

const (
	namingUnknown naming = iota // nothing to judge by
	namingAgrees                // Claude Code names cwd's project folder the way encode does
	namingOther                 // the session lives in another folder's project folder
	namingDrift                 // Claude Code names project folders differently than encode
)

// checkNaming compares the project folder Claude Code actually uses for this
// session (the directory of its transcript) with the name encode predicts.
// got is that folder's name and folder the path it was judged by.
//
// A different name alone proves nothing: a session resumed from another git
// worktree stays in that worktree's project folder. The scheme is only in
// doubt when the folder recorded inside would not get this name either.
func checkNaming(cwd, transcript string) (n naming, got, folder string) {
	if cwd == "" || transcript == "" {
		return namingUnknown, "", ""
	}
	dir := filepath.Dir(transcript)
	if realPath(filepath.Dir(dir)) != realPath(projectsDir()) {
		return namingUnknown, "", ""
	}
	got = filepath.Base(dir)
	if got == encode(cwd) {
		return namingAgrees, got, cwd
	}
	rec := recordedCwd(dir)
	switch {
	case rec == "" || len(sanitize(rec)) > maxName: // empty so far, or a hashed long name
		return namingUnknown, got, ""
	case encode(rec) == got:
		return namingOther, got, rec
	}
	return namingDrift, got, rec
}

// hasEarlierSessions reports whether cwd has sessions other than the one starting.
func hasEarlierSessions(cwd, current string) bool {
	for _, t := range transcripts(filepath.Join(projectsDir(), encode(cwd))) {
		if strings.TrimSuffix(filepath.Base(t), ".jsonl") != current {
			return true
		}
	}
	return false
}

// sameDir reports whether two paths lead to one directory.
func sameDir(a, b string) bool {
	ia, err1 := statIdent(a)
	ib, err2 := statIdent(b)
	return err1 == nil && err2 == nil && ia == ib
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
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
	if os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME") != "" {
		return 0, nil // this session's data has a fixed name: it does not depend on the path
	}
	var reports []*Report
	var here *Report
	var warnings []string
	err := withLock(10*time.Second, func() error {
		reg := loadRegistry()
		kind, got, folder := checkNaming(cwd, in.TranscriptPath)
		switch kind {
		case namingDrift:
			reg.setDrift(folder, got)
		case namingAgrees:
			reg.clearDrift()
		}
		if reg.Drift != nil {
			warnings = append(warnings, "automatic following is paused: "+reg.Drift.String()+
				"; update intow (the naming scheme of Claude Code changed)")
			return reg.save()
		}
		if id, err := statIdent(cwd); err == nil && !isTemp(cwd) {
			was := ""
			// Is the folder Claude is starting in one that was moved here?
			if _, e := reg.find(id); e != nil && e.Path != cwd {
				if samePath(e.Path, cwd) || sameDir(e.Path, cwd) {
					// The recorded path still leads here: the same folder under
					// another spelling. Only a change of case is a rename;
					// two mount points or drive-letter cases are both right.
					if caseRename(e.Path, cwd) {
						rep, err := migrate(e.Path, cwd, migrateOpts{ExcludeSession: in.SessionID, Reg: reg})
						if rep != nil {
							reports = append(reports, rep)
							here = rep
						}
						if err != nil {
							return err
						}
					}
				} else if exists(e.Path) && hasEarlierSessions(cwd, in.SessionID) {
					// Another folder has the old path, and this one already has
					// its history here: it was followed from another config dir
					// that shares the projects folder. Only the settings of this
					// config dir are left to catch up.
					n, err := copySettings(e.Path, cwd)
					if n > 0 {
						reports = append(reports, &Report{Old: e.Path, New: cwd, ConfigKeys: n})
					}
					if err != nil {
						return err
					}
				} else if exists(e.Path) {
					was = e.Path
					warnings = append(warnings, fmt.Sprintf(
						"this folder used to be %s, which now holds another folder; if the history recorded there belongs here, "+
							"`intow link --force %s %s` moves all of it (`-n` shows what first)",
						e.Path, shellQuote(e.Path), shellQuote(cwd)))
				} else {
					// Put the data where Claude keeps this session, unless that
					// is the project folder of some other folder.
					dst := ""
					if in.TranscriptPath != "" && kind != namingOther {
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
			if here != nil && len(here.Busy) > 0 {
				// The history still waits under the old path. Recording this path
				// now would forget that it is owed, and nothing would retry it.
			} else if key, err := reg.track(cwd); err == nil && was != "" {
				reg.Entries[key].Was = was
				reg.dirty = true
			}
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
	if here != nil && here.changed() && os.Getenv("CLAUDE_CODE_ENTRYPOINT") == "claude-vscode" {
		// The editor extension drew its session list before this hook ran, and
		// only reads it again when asked.
		parts = append(parts, "reopen or refresh the session list to see the earlier sessions")
	}
	if here != nil && len(here.Busy) > 0 {
		parts = append(parts, fmt.Sprintf("the history of %s waits until Claude quits there (pid %s); "+
			"it moves here at the next start after that", here.Old, strings.Join(here.Busy, ", ")))
	}
	parts = append(parts, warnings...)
	if len(parts) == 0 {
		return 0, nil
	}
	out := map[string]any{"systemMessage": "intow: " + strings.Join(parts, "; ")}
	if here != nil && here.changed() {
		ctx := fmt.Sprintf("This project folder was moved from %s to %s. intow re-attached its Claude Code history "+
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
