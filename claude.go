package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func transcripts(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// recordedCwd is the folder a project dir belongs to: the first "cwd" in its
// newest transcript (the cwd of later records can drift into subfolders).
func recordedCwd(dir string) string {
	files := transcripts(dir)
	mtime := map[string]time.Time{}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			mtime[f] = fi.ModTime()
		}
	}
	sort.Slice(files, func(i, j int) bool { return mtime[files[i]].After(mtime[files[j]]) })
	for _, p := range files {
		if cwd := firstCwd(p); cwd != "" {
			return cwd
		}
	}
	return ""
}

func firstCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	for i := 0; i < 200; i++ {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"cwd"`)) {
			var rec struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(line, &rec) == nil && filepath.IsAbs(rec.Cwd) {
				return rec.Cwd
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

type projectDir struct {
	name string // directory name under ~/.claude/projects
	cwd  string // folder it belongs to
}

// projectDirsUnder lists Claude project dirs for root and its subfolders,
// shortest path first.
func projectDirsUnder(root string) []projectDir {
	ents, err := os.ReadDir(projectsDir())
	if err != nil {
		return nil
	}
	exact := encode(root)
	prefix := sanitize(root)
	if len(prefix) > maxName {
		prefix = prefix[:maxName]
	}
	var out []projectDir
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		cwd := recordedCwd(filepath.Join(projectsDir(), name))
		switch {
		case name == exact && (cwd == "" || under(cwd, root)):
			out = append(out, projectDir{name, root})
		case cwd != "" && cwd != root && under(cwd, root) && encode(cwd) == name:
			out = append(out, projectDir{name, cwd})
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].cwd) < len(out[j].cwd) })
	return out
}

type liveSession struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
}

// liveSessions reads ~/.claude/sessions/<pid>.json for running Claude processes.
func liveSessions() []liveSession {
	dir := filepath.Join(configDir(), "sessions")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []liveSession
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s liveSession
		if json.Unmarshal(b, &s) == nil && processAlive(s.PID) {
			out = append(out, s)
		}
	}
	return out
}

// memoryIndex finds MEMORY.md for a folder. Auto-memory is keyed by the git
// root when there is one, otherwise by the folder itself.
func memoryIndex(cwd, transcriptPath string) string {
	var cands []string
	if transcriptPath != "" {
		cands = append(cands, filepath.Dir(transcriptPath))
	}
	cands = append(cands, filepath.Join(projectsDir(), encode(cwd)))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			cands = append(cands, filepath.Join(projectsDir(), encode(top)))
		}
	}
	for _, c := range cands {
		p := filepath.Join(c, "memory", "MEMORY.md")
		if isFile(p) {
			return p
		}
	}
	return ""
}
