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
	// Folders whose old path another folder took before their history followed.
	var displaced [][2]string
	for _, a := range plan(reg) {
		if a.State == "replaced" {
			if a.New != "" {
				displaced = append(displaced, [2]string{a.Old, a.New})
			}
			continue // the path itself is fine: another folder lives there
		}
		moved[a.Old] = planned{a.State, a.New}
	}
	for _, k := range reg.sortedKeys() {
		if e := reg.Entries[k]; e.Was != "" {
			displaced = append(displaced, [2]string{e.Was, e.Path})
		}
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
	shown := false
	for _, d := range displaced {
		if len(projectDirsUnder(d[0])) == 0 {
			continue
		}
		if !shown {
			c.printf("\nMoved aside, and another folder now has the old path. If the history recorded under the old path\n" +
				"belongs to the moved folder, this moves all of it (check with -n first):\n")
			shown = true
		}
		c.printf("  ccanchor link --force %s %s\n", shellQuote(d[0]), shellQuote(d[1]))
	}
	if reg.Drift != nil {
		c.printf("\nAutomatic following is paused: %s.\nUpdate ccanchor; it resumes when the names agree again.\n", reg.Drift)
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
	var paused *Drift
	run := func() error {
		reg := loadRegistry()
		if paused = reg.Drift; paused != nil && apply {
			return nil
		}
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
	if paused != nil && apply {
		if quiet {
			return 0, nil
		}
		return 1, fmt.Errorf("paused: %s; update ccanchor", paused)
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
		if reg.Drift != nil && !dry && !force {
			return fmt.Errorf("paused: %s; update ccanchor (or pass --force)", reg.Drift)
		}
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
	// On a case-insensitive volume `mv app App` names the folder itself.
	caseOnly := src != dst && sameFile(src, dst)
	if isDir(dst) && !caseOnly {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	if exists(dst) && !caseOnly {
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
	if d := loadRegistry().Drift; d != nil && !dry && !force {
		return 1, fmt.Errorf("paused: %s; update ccanchor (or pass --force)", d)
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

// moveFolder renames, or across volumes hands over to the platform.
func moveFolder(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !isCrossDevice(err) {
		return err
	}
	return crossDeviceMove(src, dst)
}

// ---- undo

type logEntry struct {
	TS int64 `json:"ts"`
	Report
}

func readLog() []logEntry {
	f, err := os.Open(filepath.Join(stateDir(), "log.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []logEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var d logEntry
		if json.Unmarshal(sc.Bytes(), &d) == nil && d.Old != "" && d.New != "" {
			out = append(out, d)
		}
	}
	return out
}

// cmdUndo takes the most recent migration back, or the most recent one of a
// given folder: what it moved returns under the old path, unless Claude has
// continued a session or changed a setting in the new place since. The folder
// is no longer tracked afterwards, so it is not followed there again.
func (c *cli) cmdUndo(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"n", "dry-run", "f", "force"}, nil)
	if err != nil {
		return 2, err
	}
	if len(o.args) > 1 {
		return 2, errors.New("usage: ccanchor undo [-n] [-f] [FOLDER]")
	}
	folder := ""
	if len(o.args) == 1 {
		folder = absClean(o.args[0])
	}
	dry, force := o.has("n", "dry-run"), o.has("f", "force")
	var j *journal
	var u *undone
	run := func() error {
		if j = lastJournal(folder); j == nil {
			return nil
		}
		if !force {
			for _, s := range liveSessions() {
				if under(s.Cwd, j.New) {
					return fmt.Errorf("Claude is running in %s (pid %d); quit it first or pass --force", j.New, s.PID)
				}
			}
		}
		var err error
		if u, err = j.undo(dry); err != nil || dry {
			return err
		}
		reg := loadRegistry()
		for k, e := range reg.Entries {
			if under(e.Path, j.New) || under(e.Path, j.Old) {
				delete(reg.Entries, k)
				reg.dirty = true
			}
		}
		j.markUndone()
		if u.changed() {
			appendLog(&Report{Old: j.New, New: j.Old, Sessions: u.Sessions, Memory: u.Memory,
				ConfigKeys: u.ConfigKeys, RepoPaths: u.RepoPaths, Plugins: u.Plugins, History: u.History, Undo: true})
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
	if j == nil {
		if folder != "" {
			c.printf("no migration of %s to undo\n", folder)
		} else {
			c.printf("no migration to undo\n")
		}
		return 1, nil
	}
	head := "undid"
	if dry {
		head = "would undo"
	}
	var bits []string
	if u.Sessions > 0 {
		bits = append(bits, plural(u.Sessions, "session"))
	}
	if u.Memory > 0 {
		bits = append(bits, plural(u.Memory, "memory file"))
	}
	if u.ConfigKeys > 0 || u.RepoPaths > 0 {
		bits = append(bits, "settings")
	}
	if u.Plugins > 0 {
		bits = append(bits, plural(u.Plugins, "plugin install"))
	}
	if u.History > 0 {
		bits = append(bits, plural(u.History, "history line"))
	}
	if len(bits) == 0 {
		bits = []string{"nothing left to take back"}
	}
	c.printf("%s %s -> %s of %s: %s back under %s\n", head, j.Old, j.New,
		time.Unix(j.TS, 0).Format("2006-01-02 15:04:05"), strings.Join(bits, ", "), j.Old)
	for _, n := range u.Notes {
		c.printf("    note: %s\n", n)
	}
	if !dry {
		c.printf("%s is no longer tracked; `ccanchor link %s %s` attaches the history to it again\n",
			j.New, shellQuote(j.Old), shellQuote(j.New))
	}
	return 0, nil
}

// ---- shell-init

// stableSelf is the path a shell function should call: `ccanchor` when that
// is this binary, else the version-independent name next to the plugin's
// binary, else the binary itself.
func stableSelf() string {
	self, err := os.Executable()
	if err != nil {
		return "ccanchor"
	}
	real, err := filepath.EvalSymlinks(self)
	if err != nil {
		real = self
	}
	if p, err := exec.LookPath("ccanchor"); err == nil {
		if r, err := filepath.EvalSymlinks(p); err == nil && r == real {
			return "ccanchor"
		}
	}
	if alias := filepath.Join(filepath.Dir(real), "ccanchor"+exeSuffix); alias != real {
		if r, err := filepath.EvalSymlinks(alias); err == nil && r == real {
			return alias
		}
	}
	return real
}

// cmdShellInit prints a `claude` shell function that follows moved folders
// before Claude Code starts. The SessionStart hook is too late for
// `claude --continue` and `--resume`: Claude looks for the session first and
// gives up ("No conversation found to continue") before any hook runs.
func (c *cli) cmdShellInit(argv []string) (int, error) {
	o, err := parseArgs(argv, nil, nil)
	if err != nil {
		return 2, err
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	if shell == "." && exeSuffix != "" {
		shell = "powershell" // no $SHELL on Windows outside Git Bash
	}
	if len(o.args) > 0 {
		shell = o.args[0]
	}
	self := stableSelf()
	if shell == "powershell" || shell == "pwsh" {
		// Get-Command -CommandType Application skips this function and finds claude.exe or claude.cmd.
		call := "ccanchor"
		have := "Get-Command ccanchor -CommandType Application -ErrorAction SilentlyContinue"
		if filepath.IsAbs(self) {
			q := "'" + strings.ReplaceAll(self, "'", "''") + "'"
			call, have = "& "+q, "Test-Path -LiteralPath "+q
		}
		c.printf("function claude {\n  if (%s) { %s sync --apply -q }\n"+
			"  $real = Get-Command claude -CommandType Application -ErrorAction Stop | Select-Object -First 1\n"+
			"  & $real.Source @args\n}\n", have, call)
		return 0, nil
	}
	if exeSuffix != "" {
		self = filepath.ToSlash(self) // Git Bash
	}
	bin := shellQuote(self)
	switch shell {
	case "fish":
		have := "command -q " + bin
		if filepath.IsAbs(self) {
			have = "test -x " + bin
		}
		c.printf("function claude --wraps claude --description 'Claude Code, after ccanchor followed moved folders'\n"+
			"    %s; and %s sync --apply -q\n    command claude $argv\nend\n", have, bin)
	case "zsh", "bash", "sh", "ksh", "dash":
		have := "command -v " + bin + " >/dev/null 2>&1"
		if filepath.IsAbs(self) {
			have = "[ -x " + bin + " ]"
		}
		c.printf("claude() {\n  %s && %s sync --apply -q\n  command claude \"$@\"\n}\n", have, bin)
	default:
		return 2, fmt.Errorf("unknown shell %q: use zsh, bash, fish or powershell", shell)
	}
	return 0, nil
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
	entries := readLog()
	if len(entries) == 0 {
		c.printf("no migrations yet\n")
		return 0, nil
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	for _, d := range entries {
		kind := ""
		if d.Undo {
			kind = "  [undo]"
		}
		c.printf("%s  %s -> %s  (%d sessions, %d memory, %d settings)%s\n",
			time.Unix(d.TS, 0).Format("2006-01-02 15:04:05"), d.Old, d.New, d.Sessions, d.Memory, d.ConfigKeys, kind)
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
