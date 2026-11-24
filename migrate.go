package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Report describes one migration of a folder's Claude Code data.
type Report struct {
	Old        string      `json:"old"`
	New        string      `json:"new"`
	Dirs       [][3]string `json:"dirs"` // source name, destination name, mode
	Sessions   int         `json:"sessions"`
	Memory     int         `json:"memory"`
	ConfigKeys int         `json:"config_keys"`
	RepoPaths  int         `json:"repo_paths,omitempty"`   // githubRepoPaths in ~/.claude.json
	Plugins    int         `json:"plugin_paths,omitempty"` // project-scope plugin installs
	History    int         `json:"history"`
	Conflicts  []string    `json:"conflicts,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	Undo       bool        `json:"undo,omitempty"` // this entry took an earlier migration back
	Skipped    string      `json:"-"`

	j *journal // what was done, for `ccanchor undo`; nil in dry runs
}

func (r *Report) changed() bool {
	return len(r.Dirs) > 0 || r.ConfigKeys > 0 || r.RepoPaths > 0 || r.Plugins > 0 || r.History > 0
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func (r *Report) short() string {
	var bits []string
	if r.Sessions > 0 {
		bits = append(bits, plural(r.Sessions, "session"))
	}
	if r.Memory > 0 {
		bits = append(bits, plural(r.Memory, "memory file"))
	}
	if r.ConfigKeys > 0 || r.RepoPaths > 0 {
		bits = append(bits, "settings")
	}
	if r.Plugins > 0 {
		bits = append(bits, plural(r.Plugins, "plugin install"))
	}
	if len(bits) == 0 {
		bits = []string{"no data"}
	}
	return fmt.Sprintf("%s -> %s (%s)", r.Old, r.New, strings.Join(bits, ", "))
}

func (r *Report) lines(dryRun bool) []string {
	if r.Skipped != "" {
		return []string{fmt.Sprintf("skip %s -> %s: %s", r.Old, r.New, r.Skipped)}
	}
	head := "moved"
	if dryRun {
		head = "would move"
	}
	out := []string{head + " " + r.short()}
	for _, d := range r.Dirs {
		out = append(out, fmt.Sprintf("    projects/%s -> %s  [%s]", d[0], d[1], d[2]))
	}
	if r.History > 0 {
		out = append(out, fmt.Sprintf("    history.jsonl: %d line(s)", r.History))
	}
	if r.RepoPaths > 0 {
		out = append(out, fmt.Sprintf("    githubRepoPaths: %d path(s)", r.RepoPaths))
	}
	if r.Plugins > 0 {
		out = append(out, fmt.Sprintf("    installed_plugins.json: %d install(s)", r.Plugins))
	}
	for _, c := range r.Conflicts {
		out = append(out, "    ! left in place (name clash): "+c)
	}
	for _, n := range r.Notes {
		out = append(out, "    note: "+n)
	}
	return out
}

type migrateOpts struct {
	DryRun         bool
	DstOverride    string // project dir Claude already uses for the new path (from the hook)
	ExcludeSession string
	Force          bool
	Reg            *Registry
}

// migrate re-keys all Claude Code data of folder old (and its subfolders) to new.
func migrate(old, new string, o migrateOpts) (*Report, error) {
	rep := &Report{Old: old, New: new}
	if old == new || (samePath(old, new) && !caseRename(old, new)) {
		rep.Skipped = "same path"
		return rep, nil
	}
	if !samePath(old, new) && (under(new, old) || under(old, new)) {
		rep.Skipped = "one path is inside the other"
		return rep, nil
	}
	if !o.Force {
		var pids []string
		for _, s := range liveSessions() {
			if under(s.Cwd, old) && s.SessionID != o.ExcludeSession {
				pids = append(pids, strconv.Itoa(s.PID))
			}
		}
		if len(pids) > 0 {
			rep.Skipped = "Claude is still running there (pid " + strings.Join(pids, ", ") + "); will retry later"
			return rep, nil
		}
	}
	if !o.DryRun {
		rep.j = newJournal(old, new)
		defer rep.j.save() // also after an error: what was done so far can be taken back
	}
	for _, pd := range projectDirsUnder(old) {
		// pd.cwd may spell old its own way (Windows): the new name keeps that spelling.
		dst := encode(respell(rebase(pd.cwd, old, new), pd.cwd))
		if o.DstOverride != "" && pd.cwd == old && filepath.Dir(o.DstOverride) == projectsDir() {
			dst = filepath.Base(o.DstOverride)
		}
		src := filepath.Join(projectsDir(), pd.name)
		if err := moveProjectDir(src, filepath.Join(projectsDir(), dst), old, new, rep, o.DryRun); err != nil {
			return rep, err
		}
	}
	if err := updateGlobalJSON(old, new, rep, o.DryRun); err != nil {
		return rep, err
	}
	if err := updateInstalledPlugins(old, new, rep, o.DryRun); err != nil {
		return rep, err
	}
	if err := updateHistory(old, new, rep, o.DryRun); err != nil {
		return rep, err
	}
	if o.Reg != nil {
		o.Reg.rebase(old, new)
	}
	if !o.DryRun && rep.changed() {
		appendLog(rep)
	}
	return rep, nil
}

func moveProjectDir(src, dst, old, new string, rep *Report, dryRun bool) error {
	sname, dname := filepath.Base(src), filepath.Base(dst)
	same := src == dst // encoding unchanged (e.g. "a_b" -> "a-b"): only the recorded cwd changes
	if !same && sameFile(src, dst) {
		// One directory under two spellings: the folder's name changed only in
		// case, on a case-insensitive volume. Give it the new spelling.
		same = true
		if !dryRun {
			os.Rename(src, dst)
		}
	}
	if same {
		rep.Sessions += len(transcripts(dst))
		if !dryRun {
			if _, err := rewriteTree(dst, old, new, rep.j); err != nil {
				return err
			}
			checkRewritten(dst, old, rep)
		}
		rep.Dirs = append(rep.Dirs, [3]string{sname, dname, "in place"})
		return nil
	}
	if !exists(dst) {
		renamed := true
		if !dryRun {
			if err := os.Rename(src, dst); err != nil {
				if !isExistErr(err) {
					return err
				}
				renamed = false // Claude created dst in the meantime: merge instead
			} else {
				rep.j.movedEntries(src, dst)
			}
		}
		if renamed {
			target := dst
			if dryRun {
				target = src
			}
			rep.Sessions += len(transcripts(target))
			if mem := filepath.Join(target, "memory"); isDir(mem) {
				rep.Memory += countFiles(mem)
			}
			if !dryRun {
				if _, err := rewriteTree(dst, old, new, rep.j); err != nil {
					return err
				}
				checkRewritten(dst, old, rep)
			}
			rep.Dirs = append(rep.Dirs, [3]string{sname, dname, "rename"})
			return nil
		}
	}
	if err := mergeProjectDir(src, dst, old, new, rep, dryRun); err != nil {
		return err
	}
	rep.Dirs = append(rep.Dirs, [3]string{sname, dname, "merge"})
	return nil
}

func mergeProjectDir(src, dst, old, new string, rep *Report, dryRun bool) error {
	if sameFile(src, dst) {
		return nil
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var moved []string
	for _, de := range ents {
		name := de.Name()
		s, d := filepath.Join(src, name), filepath.Join(dst, name)
		if name == "memory" && de.IsDir() {
			if err := mergeMemory(s, d, rep, dryRun); err != nil {
				return err
			}
			continue
		}
		switch {
		case !exists(d):
			if strings.HasSuffix(name, ".jsonl") {
				rep.Sessions++
			}
			if !dryRun {
				if err := os.Rename(s, d); err != nil {
					return err
				}
				rep.j.move(s, d)
				moved = append(moved, d)
			}
		case isFile(s) && isFile(d) && !sameFile(s, d) && filesEqual(s, d):
			if !dryRun {
				rep.j.drop(s, d)
				os.Remove(s)
			}
		default:
			rep.Conflicts = append(rep.Conflicts, s)
		}
	}
	for _, p := range moved {
		if _, err := rewriteTree(p, old, new, rep.j); err != nil {
			return err
		}
	}
	if !dryRun {
		os.Remove(src) // only succeeds when empty
	}
	return nil
}

func mergeMemory(s, d string, rep *Report, dryRun bool) error {
	if sameFile(s, d) {
		return nil
	}
	if !exists(d) {
		rep.Memory += countFiles(s)
		if dryRun {
			return nil
		}
		if err := os.Rename(s, d); err != nil {
			return err
		}
		rep.j.move(s, d)
		return nil
	}
	ents, err := os.ReadDir(s)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.SliceStable(names, func(i, j int) bool { // MEMORY.md last, after renames are known
		return names[i] != "MEMORY.md" && names[j] == "MEMORY.md"
	})
	renamed := map[string]string{}
	for _, name := range names {
		sp, dp := filepath.Join(s, name), filepath.Join(d, name)
		switch {
		case !exists(dp):
			rep.Memory += countFiles(sp)
			if !dryRun {
				if err := os.Rename(sp, dp); err != nil {
					return err
				}
				rep.j.move(sp, dp)
			}
		case isFile(sp) && isFile(dp) && !sameFile(sp, dp) && filesEqual(sp, dp):
			if !dryRun {
				rep.j.drop(sp, dp)
				os.Remove(sp)
			}
		case name == "MEMORY.md" && isFile(sp) && isFile(dp):
			n, err := mergeIndex(sp, dp, renamed, dryRun, rep.j)
			if err != nil {
				return err
			}
			if n > 0 {
				rep.Notes = append(rep.Notes, "memory index merged")
			}
			if !dryRun {
				os.Remove(sp)
			}
		case isDir(sp) && isDir(dp):
			if err := mergeMemory(sp, dp, rep, dryRun); err != nil {
				return err
			}
		default:
			alt := uniqueName(d, name)
			renamed[name] = alt
			rep.Memory++
			rep.Notes = append(rep.Notes, fmt.Sprintf("memory/%s exists in both; old copy kept as memory/%s", name, alt))
			if !dryRun {
				if err := os.Rename(sp, filepath.Join(d, alt)); err != nil {
					return err
				}
				rep.j.move(sp, filepath.Join(d, alt))
			}
		}
	}
	if !dryRun {
		os.Remove(s)
	}
	return nil
}

// mergeIndex appends lines of the old MEMORY.md that the new one lacks,
// pointing links at renamed files.
func mergeIndex(sp, dp string, renamed map[string]string, dryRun bool, j *journal) (int, error) {
	src, err := os.ReadFile(sp)
	if err != nil {
		return 0, err
	}
	dstB, err := os.ReadFile(dp)
	if err != nil {
		return 0, err
	}
	dst := string(dstB)
	have := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(dst, "\r\n", "\n"), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, line := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
		for a, b := range renamed {
			line = strings.ReplaceAll(line, "("+a+")", "("+b+")")
		}
		if t := strings.TrimSpace(line); t != "" && !have[t] {
			add = append(add, line)
			have[t] = true
		}
	}
	if dryRun {
		return len(add), nil
	}
	after := dst
	if len(add) > 0 {
		if after != "" && !strings.HasSuffix(after, "\n") {
			after += "\n"
		}
		after += strings.Join(add, "\n") + "\n"
		if err := atomicWrite(dp, []byte(after), 0o644); err != nil {
			return 0, err
		}
	}
	// The caller removes sp: remember both indexes as they were.
	op := jop{Op: "index", Path: dp, From: sp, Sum: sumOf([]byte(after))}
	if was := string(src); len(was)+len(dst) <= maxIndexBytes {
		op.Text, op.Was = &dst, &was
	} else {
		op.Lost = true
	}
	j.add(op)
	return len(add), nil
}

func uniqueName(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	cand := stem + ".moved" + ext
	for i := 2; exists(filepath.Join(dir, cand)); i++ {
		cand = fmt.Sprintf("%s.moved%d%s", stem, i, ext)
	}
	return cand
}

// checkRewritten notes when the transcripts of a project dir still record the
// old folder after the rewrite: Claude Code then writes "cwd" in a form
// rewriteField does not know, and ccanchor needs an update.
func checkRewritten(dir, old string, rep *Report) {
	if cwd := recordedCwd(dir); cwd != "" && under(cwd, old) {
		rep.Notes = append(rep.Notes, "sessions in projects/"+filepath.Base(dir)+
			" still record the old folder (unknown transcript format); they were moved but not rewritten")
	}
}

func jsonFragment(s string) []byte {
	b, _ := encodeJSON(s, false)
	return b[1 : len(b)-1]
}

// swapField rewrites `"field":"<old>"` and `"field":"<old>/...` to the new
// prefix. pre counts the values that named the new folder already.
func swapField(data []byte, field, old, new string) (out []byte, count, pre int) {
	type pattern struct{ from, to []byte }
	var pats []pattern
	for _, v := range variants(old, new) { // one pair, except on Windows
		o, n := jsonFragment(v[0]), jsonFragment(v[1])
		next := sep // what follows the folder in the path of a subfolder
		if !strings.Contains(v[0], sep) && strings.Contains(v[0], "/") {
			next = "/"
		}
		// Claude Code writes compact JSON; the spaced form is for other writers.
		for _, key := range [][]byte{[]byte(`"` + field + `":"`), []byte(`"` + field + `": "`)} {
			for _, term := range [][]byte{[]byte(`"`), jsonFragment(next)} {
				pats = append(pats, pattern{bytes.Join([][]byte{key, o, term}, nil), bytes.Join([][]byte{key, n, term}, nil)})
			}
		}
	}
	for _, p := range pats {
		pre += bytes.Count(data, p.to)
	}
	for _, p := range pats {
		if c := bytes.Count(data, p.from); c > 0 {
			data = bytes.ReplaceAll(data, p.from, p.to)
			count += c
		}
	}
	return data, count, pre
}

// rewriteField applies swapField to a JSONL file. Message text is history and
// stays as it was, and the file keeps its mtime so /resume keeps its order.
func rewriteField(path, field, old, new string) (count, pre int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	data, count, pre = swapField(data, field, old, new)
	if count == 0 {
		return 0, pre, nil
	}
	return count, pre, writeKeepingMtime(path, data)
}

func writeKeepingMtime(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, data, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(path, fi.ModTime(), fi.ModTime())
}

// rewriteLines is rewriteField line by line, for a file that many folders
// share. With only it touches just the lines whose hash is listed. It returns
// the hashes of the lines as it left them, which is what undoes it later.
func rewriteLines(path, field, old, new string, only map[string]bool, dryRun bool) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out bytes.Buffer
	var sums []string
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		body := bytes.TrimRight(line, "\r\n")
		if len(body) > 0 && (only == nil || only[sumOf(body)]) {
			if nb, c, _ := swapField(body, field, old, new); c > 0 {
				sums = append(sums, sumOf(nb))
				out.Write(nb)
				out.Write(line[len(body):])
				continue
			}
		}
		out.Write(line)
	}
	if len(sums) == 0 || dryRun {
		return sums, nil
	}
	return sums, writeKeepingMtime(path, out.Bytes())
}

// restoreLines reverses rewriteLines for the lines it changed, unless they
// have changed again since.
func restoreLines(path, field, new, old string, only map[string]bool, dryRun bool) (int, error) {
	if len(only) == 0 {
		return 0, nil
	}
	sums, err := rewriteLines(path, field, new, old, only, dryRun)
	return len(sums), err
}

func rewriteTree(root, old, new string, j *journal) (int, error) {
	total := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		n, pre, err := rewriteField(p, "cwd", old, new)
		if n > 0 && err == nil {
			j.rewrote(p, "cwd", pre)
		}
		total += n
		return err
	})
	return total, err
}

// rekeyGlobalJSON moves the per-folder entries of ~/.claude.json ("projects"
// keys and the folders listed under "githubRepoPaths") from old to new. It
// returns nil when nothing refers to old.
func rekeyGlobalJSON(raw []byte, old, new string) (out []byte, keys, repos int, ops []jop) {
	root, err := parseObject(raw)
	if err != nil {
		return nil, 0, 0, nil
	}
	if pj, ok := root.get("projects"); ok {
		if projects, err := parseObject(pj); err == nil {
			var moved []string
			for _, k := range projects.keys {
				if _, ok := rebasePath(k, old, new); ok {
					moved = append(moved, k)
				}
			}
			for _, k := range moved {
				nk, _ := rebasePath(k, old, new)
				keys++
				v := projects.vals[k]
				projects.del(k)
				op := jop{Op: "config", From: k, To: nk, Before: compactJSON(v)}
				if cur, ok := projects.get(nk); ok {
					op.Had = compactJSON(cur)
					projects.set(nk, mergeSettings(v, cur))
				} else {
					projects.set(nk, v)
				}
				op.After = compactJSON(projects.vals[nk])
				ops = append(ops, op)
			}
			if keys > 0 {
				root.set("projects", projects.marshal())
			}
		}
	}
	if rp, ok := root.get("githubRepoPaths"); ok {
		if byRepo, err := parseObject(rp); err == nil {
			for _, name := range byRepo.keys {
				var paths []string
				if json.Unmarshal(byRepo.vals[name], &paths) != nil {
					continue
				}
				n, seen, kept := 0, map[string]bool{}, paths[:0]
				for _, p := range paths {
					if np, ok := rebasePath(p, old, new); ok {
						p = np
						n++
					}
					if !seen[p] {
						seen[p] = true
						kept = append(kept, p)
					}
				}
				if n > 0 {
					b, _ := encodeJSON(kept, false)
					ops = append(ops, jop{Op: "repos", Name: name, Before: compactJSON(byRepo.vals[name]), After: b})
					byRepo.set(name, b)
					repos += n
				}
			}
			if repos > 0 {
				root.set("githubRepoPaths", byRepo.marshal())
			}
		}
	}
	if keys == 0 && repos == 0 {
		return nil, 0, 0, nil
	}
	out = pretty(root.marshal())
	if bytes.HasSuffix(raw, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, keys, repos, ops
}

func updateGlobalJSON(old, new string, rep *Report, dryRun bool) error {
	path := globalJSONPath()
	if dryRun {
		if raw, err := os.ReadFile(path); err == nil {
			_, rep.ConfigKeys, rep.RepoPaths, _ = rekeyGlobalJSON(raw, old, new)
		}
		return nil
	}
	var ops []jop // of the attempt that was written
	err := editClaudeJSON(path, func(raw []byte) []byte {
		out, keys, repos, o := rekeyGlobalJSON(raw, old, new)
		if out != nil {
			rep.ConfigKeys, rep.RepoPaths, ops = keys, repos, o
		}
		return out
	})
	if err == nil {
		for _, op := range ops {
			rep.j.add(op)
		}
	}
	return err
}

// rebasePluginInstalls rewrites the folder that project- and local-scope
// plugin installs are recorded for in plugins/installed_plugins.json.
func rebasePluginInstalls(raw []byte, old, new string) (out []byte, n int, ops []jop) {
	root, err := parseObject(raw)
	if err != nil {
		return nil, 0, nil
	}
	pl, ok := root.get("plugins")
	if !ok {
		return nil, 0, nil
	}
	plugins, err := parseObject(pl)
	if err != nil {
		return nil, 0, nil
	}
	for _, name := range plugins.keys {
		var installs []json.RawMessage
		if json.Unmarshal(plugins.vals[name], &installs) != nil {
			continue
		}
		changed := false
		for i, inst := range installs {
			m, err := parseObject(inst)
			if err != nil {
				continue
			}
			var dir string
			if v, ok := m.get("projectPath"); !ok || json.Unmarshal(v, &dir) != nil {
				continue
			}
			ndir, ok := rebasePath(dir, old, new)
			if !ok {
				continue
			}
			b, _ := encodeJSON(ndir, false)
			m.set("projectPath", b)
			installs[i] = m.marshal()
			changed = true
			n++
		}
		if changed {
			b, _ := encodeJSON(installs, false)
			ops = append(ops, jop{Op: "plugins", Name: name, Before: compactJSON(plugins.vals[name]), After: compactJSON(b)})
			plugins.set(name, b)
		}
	}
	if n == 0 {
		return nil, 0, nil
	}
	root.set("plugins", plugins.marshal())
	out = pretty(root.marshal())
	if bytes.HasSuffix(raw, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, n, ops
}

func updateInstalledPlugins(old, new string, rep *Report, dryRun bool) error {
	path := filepath.Join(configDir(), "plugins", "installed_plugins.json")
	if dryRun {
		if raw, err := os.ReadFile(path); err == nil {
			_, rep.Plugins, _ = rebasePluginInstalls(raw, old, new)
		}
		return nil
	}
	var ops []jop
	err := editClaudeJSON(path, func(raw []byte) []byte {
		out, n, o := rebasePluginInstalls(raw, old, new)
		if out != nil {
			rep.Plugins, ops = n, o
		}
		return out
	})
	if err == nil {
		for _, op := range ops {
			rep.j.add(op)
		}
	}
	return err
}

func updateHistory(old, new string, rep *Report, dryRun bool) error {
	path := filepath.Join(configDir(), "history.jsonl")
	if !isRegular(path) {
		return nil
	}
	sums, err := rewriteLines(path, "project", old, new, nil, dryRun)
	rep.History = len(sums)
	if err == nil && len(sums) > 0 {
		op := jop{Op: "history", Path: path, Field: "project", Lines: sums}
		if len(sums) > maxHistoryLines {
			op.Lines, op.Lost = nil, true
		}
		rep.j.add(op)
	}
	return err
}

func appendLog(rep *Report) {
	if os.MkdirAll(stateDir(), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), "log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := encodeJSON(struct {
		TS int64 `json:"ts"`
		*Report
	}{time.Now().Unix(), rep}, false)
	if err == nil {
		f.Write(append(b, '\n'))
	}
}

// syncAll follows every tracked folder that moved. Without apply it works on
// a copy of the registry and only reports.
func syncAll(reg *Registry, apply bool, excludeSession string) ([]*Report, error) {
	work := reg
	if !apply {
		work = reg.clone()
	}
	var reports []*Report
	tried := map[string]bool{}
	for i := 0; i < 100; i++ {
		var next *action
		for _, a := range plan(work) {
			if a.State == "moved" && !tried[a.Key] {
				next = &a
				break
			}
		}
		if next == nil {
			break
		}
		tried[next.Key] = true
		// A skipped migration (Claude still running in the old path) leaves the
		// entry at the old path, so the next run retries it.
		rep, err := migrate(next.Old, next.New, migrateOpts{DryRun: !apply, ExcludeSession: excludeSession, Reg: work})
		reports = append(reports, rep)
		if err != nil {
			return reports, err
		}
		if rep.Skipped == "" && isDir(next.New) && !isTemp(next.New) {
			work.track(next.New)
		}
	}
	return reports, nil
}
