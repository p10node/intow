package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (c *cli) printReports(reports []*Report, dryRun, quiet bool) bool {
	shown := false
	for _, r := range reports {
		if quiet && !r.changed() && r.Skipped == "" {
			continue
		}
		for _, l := range r.lines(dryRun) {
			c.printf("%s\n", l)
		}
		shown = true
	}
	return shown
}

// ---- status

type statusRow struct {
	State    string `json:"state"`
	Path     string `json:"path"`
	Sessions int    `json:"sessions"`
	Memory   int    `json:"memory"`
	NowAt    string `json:"now_at,omitempty"`
	Suggest  string `json:"suggest,omitempty"`
}

func (c *cli) cmdStatus(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"json"}, nil)
	if err != nil {
		return 2, err
	}
	reg := loadRegistry()
	type planned struct{ state, new string }
	moved := map[string]planned{}
	for _, a := range plan(reg) {
		moved[a.Old] = planned{a.State, a.New}
	}
	byEnc := map[string]string{}
	tracked := map[string]bool{}
	for _, e := range reg.Entries {
		byEnc[encode(e.Path)] = e.Path
		tracked[e.Path] = true
	}
	gjKeys := globalProjectKeys()
	gjByEnc := map[string]string{}
	for _, k := range gjKeys {
		gjByEnc[encode(k)] = k
	}
	ents, _ := os.ReadDir(projectsDir())
	var rows []statusRow
	known := map[string]bool{}
	for _, de := range ents {
		if !de.IsDir() {
			continue
		}
		full := filepath.Join(projectsDir(), de.Name())
		path := recordedCwd(full)
		if path == "" {
			path = byEnc[de.Name()]
		}
		if path == "" {
			path = gjByEnc[de.Name()]
		}
		row := statusRow{Path: path, Sessions: len(transcripts(full))}
		if mem := filepath.Join(full, "memory"); isDir(mem) {
			row.Memory = countFiles(mem)
		}
		// the longest planned move that contains this path
		var best string
		for old := range moved {
			if under(path, old) && len(old) > len(best) {
				best = old
			}
		}
		switch {
		case path == "":
			row.State, row.Path = "unknown", de.Name()
		case isTemp(path):
			row.State = "temp"
		case best != "" && (moved[best].state == "moved" || moved[best].state == "trashed"):
			row.State, row.NowAt = moved[best].state, rebase(path, best, moved[best].new)
		case isDir(path):
			row.State = "untracked"
			for t := range tracked {
				if under(path, t) {
					row.State = "ok"
					break
				}
			}
			known[path] = true
		default:
			row.State = "orphan"
		}
		rows = append(rows, row)
	}
	for _, p := range append(gjKeys, keysOf(tracked)...) {
		if isDir(p) {
			known[p] = true
		}
	}
	for i := range rows {
		if rows[i].State == "orphan" {
			rows[i].Suggest = suggest(rows[i].Path, known)
		}
	}
	if o.has("json") {
		b, _ := encodeJSON(rows, true)
		c.printf("%s\n", b)
		return 0, nil
	}
	short := func(p string) string {
		if h := homeDir(); h != "" && under(p, h) {
			return "~" + p[len(h):]
		}
		return p
	}
	w := 4
	for _, r := range rows {
		if n := len(short(r.Path)); n > w {
			w = n
		}
	}
	c.printf("%-9s %-*s %5s %4s\n", "STATE", w, "PATH", "SESS", "MEM")
	var hints []string
	untracked, movedAny := false, false
	for _, r := range rows {
		line := fmt.Sprintf("%-9s %-*s %5d %4d", r.State, w, short(r.Path), r.Sessions, r.Memory)
		if r.NowAt != "" {
			line += "  -> " + short(r.NowAt)
		}
		c.printf("%s\n", line)
		if r.Suggest != "" {
			hints = append(hints, "ccanchor link "+shellQuote(r.Path)+" "+shellQuote(r.Suggest))
		}
		untracked = untracked || r.State == "untracked"
		movedAny = movedAny || r.State == "moved"
	}
	if movedAny {
		c.printf("\nMoved folders are followed at the next Claude start, or now with `ccanchor sync --apply`.\n")
	}
	if len(hints) > 0 {
		c.printf("\nOrphans (folder gone before ccanchor saw where it went). Likely matches, check with -n first:\n")
		for _, h := range hints {
			c.printf("  %s\n", h)
		}
	}
	if untracked {
		c.printf("\nSome folders are untracked; `ccanchor scan` records them.\n")
	}
	return 0, nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func globalProjectKeys() []string {
	raw, err := os.ReadFile(globalJSONPath())
	if err != nil {
		return nil
	}
	root, err := parseObject(raw)
	if err != nil {
		return nil
	}
	pj, ok := root.get("projects")
	if !ok {
		return nil
	}
	projects, err := parseObject(pj)
	if err != nil {
		return nil
	}
	return projects.keys
}

// suggest picks the existing folder whose name is most like the orphan's.
func suggest(orphan string, known map[string]bool) string {
	base := strings.ToLower(filepath.Base(orphan))
	best, bestScore := "", 0.75
	for _, k := range keysOf(known) {
		if k == orphan {
			continue
		}
		kb := strings.ToLower(filepath.Base(k))
		score := 1.0
		if kb != base {
			score = similarity(base, kb)
		}
		if score > bestScore || (score == bestScore && best == "") {
			best, bestScore = k, score
		}
	}
	return best
}

func similarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}

// ---- scan / forget

func (c *cli) cmdScan(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"q", "quiet"}, nil)
	if err != nil {
		return 2, err
	}
	var total, added int
	err = withLock(15*time.Second, func() error {
		reg := loadRegistry()
		before := len(reg.Entries)
		var cands []string
		ents, _ := os.ReadDir(projectsDir())
		for _, de := range ents {
			if de.IsDir() {
				cands = append(cands, recordedCwd(filepath.Join(projectsDir(), de.Name())))
			}
		}
		cands = append(cands, globalProjectKeys()...)
		for _, p := range cands {
			if p != "" && !isTemp(p) && isDir(p) {
				reg.track(p)
			}
		}
		total, added = len(reg.Entries), len(reg.Entries)-before
		return reg.save()
	})
	if err != nil {
		return 1, err
	}
	if !o.has("q", "quiet") {
		c.printf("tracking %d folder(s) (%+d)\n", total, added)
	}
	return 0, nil
}

func (c *cli) cmdForget(argv []string) (int, error) {
	o, err := parseArgs(argv, nil, nil)
	if err != nil {
		return 2, err
	}
	if len(o.args) == 0 {
		return 2, errors.New("forget needs at least one PATH")
	}
	removed := 0
	err = withLock(15*time.Second, func() error {
		reg := loadRegistry()
		for _, a := range o.args {
			p, _ := filepath.Abs(a)
			for k, e := range reg.Entries {
				if under(e.Path, p) {
					delete(reg.Entries, k)
					reg.dirty = true
					removed++
				}
			}
		}
		return reg.save()
	})
	c.printf("forgot %d folder(s)\n", removed)
	return 0, err
}

// ---- sync / link / mv

func (c *cli) cmdSync(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"apply", "q", "quiet"}, nil)
	if err != nil {
		return 2, err
	}
	apply, quiet := o.has("apply"), o.has("q", "quiet")
	var reports []*Report
	run := func() error {
		reg := loadRegistry()
		var err error
		reports, err = syncAll(reg, apply, "")
		if apply {
			if serr := reg.save(); err == nil {
				err = serr
			}
		}
		return err
	}
	if apply {
		err = withLock(15*time.Second, run)
	} else {
		err = run()
	}
	if !c.printReports(reports, !apply, quiet) && !quiet {
		c.printf("nothing moved\n")
	}
	if len(reports) > 0 && !apply {
		c.printf("\n(dry run: add --apply to do it)\n")
	}
	return 0, err
}

func absClean(p string) string {
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(homeDir(), p[2:])
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return a
}

func realDir(p string) string {
	r, err := filepath.EvalSymlinks(absClean(p))
	if err != nil {
		return absClean(p)
	}
	return r
}

func (c *cli) cmdLink(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"n", "dry-run", "f", "force"}, nil)
	if err != nil {
		return 2, err
	}
	if len(o.args) != 2 {
		return 2, errors.New("usage: ccanchor link [-n] [-f] OLD NEW")
	}
	dry, force := o.has("n", "dry-run"), o.has("f", "force")
	old, new := absClean(o.args[0]), realDir(o.args[1])
	if !isDir(new) && !force {
		return 2, fmt.Errorf("%s is not a folder (use --force to link anyway)", new)
	}
	if isDir(old) && !force {
		return 2, fmt.Errorf("%s still exists; its history would be moved away from it (use --force)", old)
	}
	var rep *Report
	run := func() error {
		reg := loadRegistry()
		opt := migrateOpts{DryRun: dry, Force: force}
		if !dry {
			opt.Reg = reg
		}
		var err error
		rep, err = migrate(old, new, opt)
		if err != nil || dry || rep.Skipped != "" {
			return err
		}
		if isDir(new) && !isTemp(new) {
			reg.track(new)
		}
		return reg.save()
	}
	if dry {
		err = run()
	} else {
		err = withLock(15*time.Second, run)
	}
	if err != nil {
		return 1, err
	}
	if !rep.changed() && rep.Skipped == "" {
		c.printf("nothing recorded for %s\n", old)
		return 1, nil
	}
	c.printReports([]*Report{rep}, dry, false)
	if rep.Skipped != "" {
		return 1, nil
	}
	return 0, nil
}

func (c *cli) cmdMv(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"n", "dry-run", "f", "force"}, nil)
	if err != nil {
		return 2, err
	}
	if len(o.args) != 2 {
		return 2, errors.New("usage: ccanchor mv [-n] [-f] SRC DST")
	}
	dry, force := o.has("n", "dry-run"), o.has("f", "force")
	src, dst := realDir(o.args[0]), absClean(o.args[1])
	if !isDir(src) {
		return 2, fmt.Errorf("%s is not a folder", src)
	}
	if isDir(dst) {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	if exists(dst) {
		return 2, fmt.Errorf("%s already exists", dst)
	}
	parent := filepath.Dir(dst)
	if !isDir(parent) {
		return 2, fmt.Errorf("%s does not exist", parent)
	}
	dst = filepath.Join(realDir(parent), filepath.Base(dst))
	if under(dst, src) {
		return 2, errors.New("cannot move a folder into itself")
	}
	if !force {
		for _, s := range liveSessions() {
			if under(s.Cwd, src) {
				return 2, fmt.Errorf("Claude is running in %s (pid %d); quit it first or pass --force", src, s.PID)
			}
		}
	}
	if dry {
		rep, err := migrate(src, dst, migrateOpts{DryRun: true, Force: true})
		if err != nil {
			return 1, err
		}
		c.printf("would mv %s %s\n", src, dst)
		c.printReports([]*Report{rep}, true, false)
		return 0, nil
	}
	var rep *Report
	err = withLock(15*time.Second, func() error {
		if err := moveFolder(src, dst); err != nil {
			return err
		}
		reg := loadRegistry()
		var err error
		rep, err = migrate(src, dst, migrateOpts{Force: true, Reg: reg})
		if err != nil {
			return err
		}
		if !isTemp(dst) {
			reg.track(dst)
		}
		return reg.save()
	})
	if err != nil {
		return 1, err
	}
	c.printf("mv %s -> %s\n", src, dst)
	if rep != nil && rep.changed() {
		c.printReports([]*Report{rep}, false, false)
	}
	return 0, nil
}

// moveFolder renames, or across volumes hands over to the system mv, which
// keeps symlinks, permissions and timestamps.
func moveFolder(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !isCrossDevice(err) {
		return err
	}
	if out, err := exec.Command("mv", "--", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("mv: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- log

func (c *cli) cmdLog(argv []string) (int, error) {
	o, err := parseArgs(argv, nil, []string{"n"})
	if err != nil {
		return 2, err
	}
	limit := 20
	if v := o.vals["n"]; v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	f, err := os.Open(filepath.Join(stateDir(), "log.jsonl"))
	if err != nil {
		c.printf("no migrations yet\n")
		return 0, nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	for _, l := range lines {
		var d struct {
			TS int64 `json:"ts"`
			Report
		}
		if json.Unmarshal([]byte(l), &d) != nil {
			continue
		}
		c.printf("%s  %s -> %s  (%d sessions, %d memory, %d settings)\n",
			time.Unix(d.TS, 0).Format("2006-01-02 15:04:05"), d.Old, d.New, d.Sessions, d.Memory, d.ConfigKeys)
	}
	return 0, nil
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == '~' || r == '+' || r == ':' || r == '@' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
