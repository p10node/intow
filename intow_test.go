package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- encoding

func TestEncodeKnown(t *testing.T) {
	cases := map[string]string{
		"/Users/p10node/WD/workplace--p10node/misc-research":       "-Users-p10node-WD-workplace--p10node-misc-research",
		"/private/var/folders/p_/3g670bz50jj4y67g7_hzrg1h0000gn/T": "-private-var-folders-p--3g670bz50jj4y67g7-hzrg1h0000gn-T",
		"/" + strings.Repeat("z", 199):                             "-" + strings.Repeat("z", 199),
	}
	for in, want := range cases {
		if got := encode(in); got != want {
			t.Errorf("encode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := len(encode("/x/" + strings.Repeat("😀", 10))); got != 23 { // surrogate pairs = 2 units
		t.Errorf("emoji length = %d", got)
	}
}

// Suffixes computed by Claude Code's own JavaScript implementation.
func TestEncodeLongPathsMatchJS(t *testing.T) {
	cases := []struct{ in, hash string }{
		{"/Users/x/" + strings.Repeat("a", 250), "bai2wj"},
		{"/Users/x/Dự án/" + strings.Repeat("thư-mục_", 30) + "😀end", "kg8cyu"},
		{"/" + strings.Repeat("z", 200), "6zubpt"},
		{"/Users/" + strings.Repeat("b/", 120), "diflwy"},
		{"/Users/x/" + strings.Repeat("Ω", 300), "hjep1v"},
	}
	for _, c := range cases {
		want := sanitize(c.in)[:200] + "-" + c.hash
		if got := encode(c.in); got != want {
			t.Errorf("encode(%.30q...) = ...%s, want ...%s", c.in, got[190:], want[190:])
		}
	}
}

// ---- sandbox

type sandbox struct {
	t                *testing.T
	root, home, work string
}

const oldMtime = 1_700_000_000

func newSandbox(t *testing.T) *sandbox {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{t: t, root: root, home: filepath.Join(root, "home"), work: filepath.Join(root, "work")}
	must(t, os.MkdirAll(filepath.Join(s.home, ".claude", "projects"), 0o755))
	must(t, os.MkdirAll(s.work, 0o755))
	t.Setenv("HOME", s.home)
	t.Setenv("USERPROFILE", s.home) // what Windows calls it
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "")
	t.Setenv("PATH", "")
	saved := tempPrefixes
	tempPrefixes = nil // the sandbox itself lives in a temp dir
	t.Cleanup(func() { tempPrefixes = saved })
	s.writeGJ(map[string]any{"projects": map[string]any{}})
	return s
}

// q is a path as it appears inside a JSON string (backslashes doubled on Windows).
func q(s string) string { return string(jsonFragment(s)) }

// sub is a subfolder path in the platform's spelling.
func sub(dir string, parts ...string) string {
	return filepath.Join(append([]string{dir}, parts...)...)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (s *sandbox) pdir(path string) string {
	return filepath.Join(s.home, ".claude", "projects", encode(path))
}

func (s *sandbox) folder(parts ...string) string {
	p := filepath.Join(append([]string{s.work}, parts...)...)
	must(s.t, os.MkdirAll(p, 0o755))
	return p
}

func (s *sandbox) writeGJ(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	must(s.t, os.WriteFile(filepath.Join(s.home, ".claude.json"), b, 0o600))
}

func (s *sandbox) gj() map[string]any {
	var v map[string]any
	b, err := os.ReadFile(filepath.Join(s.home, ".claude.json"))
	must(s.t, err)
	must(s.t, json.Unmarshal(b, &v))
	return v
}

func (s *sandbox) gjProjects() map[string]any { return s.gj()["projects"].(map[string]any) }

func (s *sandbox) session(cwd, sid string, memory map[string]string) string {
	d := s.pdir(cwd)
	must(s.t, os.MkdirAll(filepath.Join(d, "memory"), 0o755))
	recs := []map[string]any{
		{"type": "user", "cwd": cwd, "sessionId": sid, "message": map[string]any{"content": "look at " + cwd + "/x.py"}},
		{"type": "assistant", "cwd": sub(cwd, "subdir"), "sessionId": sid},
		{"type": "tool", "toolUseResult": `{"cwd":"` + q(cwd) + `"}`},
	}
	var buf bytes.Buffer
	for _, r := range recs {
		b, _ := encodeJSON(r, false)
		buf.Write(append(b, '\n'))
	}
	p := filepath.Join(d, sid+".jsonl")
	must(s.t, os.WriteFile(p, buf.Bytes(), 0o600))
	ts := time.Unix(oldMtime, 0)
	must(s.t, os.Chtimes(p, ts, ts))
	for name, text := range memory {
		must(s.t, os.WriteFile(filepath.Join(d, "memory", name), []byte(text), 0o644))
	}
	return p
}

func (s *sandbox) run(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	c := &cli{strings.NewReader(stdin), &out, &errb}
	code := c.run(args)
	return code, out.String(), errb.String()
}

func (s *sandbox) hook(cwd string) map[string]any {
	s.t.Helper()
	return s.hookAt(cwd, filepath.Join(s.pdir(cwd), "current.jsonl"))
}

// hookAt runs the hook for a session whose transcript Claude put at transcript.
func (s *sandbox) hookAt(cwd, transcript string) map[string]any {
	s.t.Helper()
	in, _ := json.Marshal(map[string]string{
		"session_id": "current", "cwd": cwd, "hook_event_name": "SessionStart",
		"transcript_path": transcript,
	})
	code, out, _ := s.run(string(in), "hook")
	if code != 0 {
		s.t.Fatalf("hook exit %d", code)
	}
	if b, err := os.ReadFile(filepath.Join(s.home, ".claude", "intow", "error.log")); err == nil {
		s.t.Fatalf("hook error log: %s", b)
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var v map[string]any
	must(s.t, json.Unmarshal([]byte(out), &v))
	return v
}

func cwds(t *testing.T, path string) []any {
	b, err := os.ReadFile(path)
	must(t, err)
	var out []any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r map[string]any
		must(t, json.Unmarshal([]byte(l), &r))
		out = append(out, r["cwd"])
	}
	return out
}

func listDir(t *testing.T, p string) []string {
	ents, err := os.ReadDir(p)
	must(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// proj/ with two sessions and memory, proj/sub/ with one session, both in ~/.claude.json.
func (s *sandbox) setupProject() string {
	proj := s.folder("proj")
	s.folder("proj", "sub")
	s.session(proj, "s1", map[string]string{
		"MEMORY.md": "- [Goal](goal.md) — the goal\n",
		"goal.md":   "---\nname: goal\n---\nship it\n",
	})
	s.session(proj, "s2", nil)
	s.session(filepath.Join(proj, "sub"), "s3", nil)
	s.writeGJ(map[string]any{"projects": map[string]any{
		proj:             map[string]any{"allowedTools": []any{"Bash(ls)"}, "hasTrustDialogAccepted": true},
		sub(proj, "sub"): map[string]any{"allowedTools": []any{}},
	}})
	s.run("", "scan", "-q")
	return proj
}

// ---- scenarios

func TestMoveAndRenameFollowedByHook(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	newp := filepath.Join(s.folder("elsewhere"), "proj-renamed")
	must(t, os.Rename(proj, newp))
	// Claude has already started in the new place: empty memory dir + current transcript.
	must(t, os.MkdirAll(filepath.Join(s.pdir(newp), "memory"), 0o755))
	must(t, os.WriteFile(filepath.Join(s.pdir(newp), "current.jsonl"), []byte(`{"type":"user","cwd":"`+q(newp)+`"}`+"\n"), 0o600))
	gj := s.gj()
	gj["projects"].(map[string]any)[newp] = map[string]any{"allowedTools": []any{"Read"}, "hasTrustDialogAccepted": false}
	s.writeGJ(gj)

	out := s.hook(newp)
	if out == nil || !strings.Contains(out["systemMessage"].(string), "proj-renamed") {
		t.Fatalf("hook output %v", out)
	}
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "3 earlier session") || !strings.Contains(ctx, "[Goal](goal.md)") {
		t.Errorf("additionalContext: %s", ctx)
	}
	if exists(s.pdir(proj)) || exists(s.pdir(sub(proj, "sub"))) {
		t.Error("old project dirs still exist")
	}
	if got := listDir(t, s.pdir(newp)); !reflect.DeepEqual(got, []string{"current.jsonl", "memory", "s1.jsonl", "s2.jsonl"}) {
		t.Errorf("new project dir: %v", got)
	}
	moved := filepath.Join(s.pdir(newp), "s1.jsonl")
	if got := cwds(t, moved); !reflect.DeepEqual(got, []any{newp, sub(newp, "subdir"), nil}) {
		t.Errorf("cwds: %v", got)
	}
	body, _ := os.ReadFile(moved)
	if !strings.Contains(string(body), q("look at "+proj+"/x.py")) {
		t.Error("message text should stay as it was")
	}
	if !strings.Contains(string(body), q(`{"cwd":"`+q(proj)+`"}`)) {
		t.Error("JSON inside strings should stay as it was")
	}
	if fi, _ := os.Stat(moved); fi.ModTime().Unix() != oldMtime {
		t.Error("mtime not kept")
	}
	if !isFile(filepath.Join(s.pdir(newp), "memory", "goal.md")) || !isFile(filepath.Join(s.pdir(sub(newp, "sub")), "s3.jsonl")) {
		t.Error("memory or subfolder session missing")
	}
	pj := s.gjProjects()
	if _, ok := pj[proj]; ok {
		t.Error("old ~/.claude.json key still there")
	}
	entry := pj[newp].(map[string]any)
	if !reflect.DeepEqual(entry["allowedTools"], []any{"Bash(ls)", "Read"}) || entry["hasTrustDialogAccepted"] != true {
		t.Errorf("merged settings: %v", entry)
	}
	if _, ok := pj[sub(newp, "sub")]; !ok {
		t.Error("subfolder key not rebased")
	}
	found := false
	for _, e := range loadRegistry().Entries {
		found = found || e.Path == newp
	}
	if !found {
		t.Error("registry not updated")
	}
	if again := s.hook(newp); again != nil {
		t.Errorf("second run should be quiet: %v", again)
	}
}

func TestSyncFromUnrelatedSessionAndDryRun(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	_, out, _ := s.run("", "sync")
	if !strings.Contains(out, "would move") || !exists(s.pdir(proj)) {
		t.Fatalf("dry run: %s", out)
	}
	res := s.hook(s.folder("other")) // starting Claude anywhere follows all moved folders
	if res == nil || !strings.Contains(res["systemMessage"].(string), "renamed") {
		t.Fatalf("hook: %v", res)
	}
	if _, ok := res["hookSpecificOutput"]; ok {
		t.Error("no context expected for an unrelated folder")
	}
	if !isFile(filepath.Join(s.pdir(newp), "s2.jsonl")) {
		t.Error("not migrated")
	}
}

func TestWaitsWhileClaudeRunsInOldPath(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	must(t, os.Rename(proj, filepath.Join(s.work, "renamed")))
	sdir := filepath.Join(s.home, ".claude", "sessions")
	must(t, os.MkdirAll(sdir, 0o755))
	live := filepath.Join(sdir, fmt.Sprintf("%d.json", os.Getpid()))
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "zzz", "cwd": proj})
	must(t, os.WriteFile(live, b, 0o644))
	_, out, _ := s.run("", "sync", "--apply")
	if !strings.Contains(out, "still running") || !exists(s.pdir(proj)) {
		t.Fatalf("expected skip: %s", out)
	}
	must(t, os.Remove(live))
	_, out, _ = s.run("", "sync", "--apply")
	if !strings.Contains(out, "moved") || exists(s.pdir(proj)) {
		t.Fatalf("expected retry to migrate: %s", out)
	}
}

func TestTrashIsNotFollowed(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	trash := filepath.Join(s.home, ".Trash")
	if runtime.GOOS == "windows" {
		trash = filepath.Join(s.root, "$Recycle.Bin", "S-1-5-21")
	}
	must(t, os.MkdirAll(trash, 0o755))
	must(t, os.Rename(proj, filepath.Join(trash, "proj")))
	_, out, _ := s.run("", "sync", "--apply")
	if !strings.Contains(out, "nothing moved") || !exists(s.pdir(proj)) {
		t.Fatalf("trash followed: %s", out)
	}
	if _, out, _ := s.run("", "status"); !strings.Contains(out, "trashed") && !scanFallback {
		t.Errorf("status: %s", out)
	}
}

func TestSameEncodingRename(t *testing.T) {
	s := newSandbox(t)
	a := s.folder("my_proj")
	s.session(a, "s1", nil)
	s.run("", "scan", "-q")
	b := filepath.Join(s.work, "my-proj")
	must(t, os.Rename(a, b))
	if encode(a) != encode(b) {
		t.Fatal("test premise")
	}
	s.run("", "sync", "--apply")
	if got := cwds(t, filepath.Join(s.pdir(b), "s1.jsonl"))[0]; got != b {
		t.Errorf("cwd = %v", got)
	}
}

func TestMvCommand(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	dest := s.folder("archive")
	if code, out, errs := s.run("", "mv", proj, dest); code != 0 {
		t.Fatalf("mv: %d %s %s", code, out, errs)
	}
	newp := filepath.Join(dest, "proj")
	if !isDir(filepath.Join(newp, "sub")) || !isFile(filepath.Join(s.pdir(newp), "s1.jsonl")) ||
		!isFile(filepath.Join(s.pdir(sub(newp, "sub")), "s3.jsonl")) {
		t.Error("mv did not carry the data")
	}
	if _, ok := s.gjProjects()[newp]; !ok {
		t.Error("settings not moved")
	}
}

func TestLinkMergesMemory(t *testing.T) {
	s := newSandbox(t)
	old := filepath.Join(s.work, "vscode-prj-managment") // gone before intow saw it
	s.session(old, "o1", map[string]string{
		"MEMORY.md": "# Index\n- [Topic](topic.md) — old hook\n- [Only old](only.md)\n",
		"topic.md":  "old body\n", "only.md": "x\n",
	})
	newp := s.folder("oss", "vscode-prj-management")
	s.session(newp, "n1", map[string]string{"MEMORY.md": "# Index\n- [Topic](topic.md) — new hook\n", "topic.md": "new body\n"})
	_, out, _ := s.run("", "status")
	if !strings.Contains(out, "orphan") || !strings.Contains(out, "intow link "+shellQuote(old)+" "+shellQuote(newp)) {
		t.Fatalf("status: %s", out)
	}
	if code, out, errs := s.run("", "link", old, newp); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	mem := filepath.Join(s.pdir(newp), "memory")
	if got := listDir(t, mem); !reflect.DeepEqual(got, []string{"MEMORY.md", "only.md", "topic.md", "topic.moved.md"}) {
		t.Errorf("memory: %v", got)
	}
	idx, _ := os.ReadFile(filepath.Join(mem, "MEMORY.md"))
	want := "# Index\n- [Topic](topic.md) — new hook\n- [Topic](topic.moved.md) — old hook\n- [Only old](only.md)\n"
	if string(idx) != want {
		t.Errorf("index:\n%s", idx)
	}
	if b, _ := os.ReadFile(filepath.Join(mem, "topic.md")); string(b) != "new body\n" {
		t.Error("new topic overwritten")
	}
	if got := transcripts(s.pdir(newp)); len(got) != 2 || exists(s.pdir(old)) {
		t.Errorf("sessions: %v", got)
	}
}

func TestLinkRefusesWhenOldExists(t *testing.T) {
	s := newSandbox(t)
	a, b := s.folder("a"), s.folder("b")
	s.session(a, "s", nil)
	if code, _, _ := s.run("", "link", a, b); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}

func TestGlobalJSONKeepsOrderAndFormat(t *testing.T) {
	s := newSandbox(t)
	old := filepath.Join(s.work, "gone")
	newp := s.folder("here")
	s.session(old, "o", nil)
	raw := "{\n  \"zeta\": 1,\n  \"projects\": {\n    \"" + q(old) + "\": {\n      \"allowedTools\": [\n        \"a && b > c\"\n      ]\n    },\n    \"/other\": {}\n  },\n  \"alpha\": 1795250653119\n}"
	must(t, os.WriteFile(filepath.Join(s.home, ".claude.json"), []byte(raw), 0o600))
	if code, out, errs := s.run("", "link", old, newp); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	got, _ := os.ReadFile(filepath.Join(s.home, ".claude.json"))
	want := "{\n  \"zeta\": 1,\n  \"projects\": {\n    \"/other\": {},\n    \"" + q(newp) + "\": {\n      \"allowedTools\": [\n        \"a && b > c\"\n      ]\n    }\n  },\n  \"alpha\": 1795250653119\n}"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestScanResolveFindsMovedDir(t *testing.T) {
	s := newSandbox(t)
	t.Setenv("HOME", s.root) // scan may climb up to the sandbox root
	t.Setenv("USERPROFILE", s.root)
	a := s.folder("a", "proj")
	id, err := statIdent(a)
	must(t, err)
	if id.Birth == 0 {
		t.Skip("filesystem has no birth time")
	}
	e := &Entry{Path: a, Dev: id.Dev, Ino: id.Ino, Hi: id.Hi, Birth: id.Birth}
	dst := filepath.Join(s.folder("b", "c"), "proj2")
	must(t, os.Rename(a, dst))
	if got := scanResolve(e, 2*time.Second); got != dst {
		t.Errorf("scanResolve = %q, want %q", got, dst)
	}
	e.Birth++ // a different birth time means a reused inode number: must not match
	if got := scanResolve(e, 2*time.Second); got != "" {
		t.Errorf("matched with wrong birth time: %q", got)
	}
}

// Hidden folders are not searched, unless the project lived in one.
func TestScanResolveGoesThroughItsOwnHiddenParents(t *testing.T) {
	s := newSandbox(t)
	t.Setenv("HOME", s.root)
	t.Setenv("USERPROFILE", s.root)
	a := s.folder(".config", "tool", "proj")
	id, err := statIdent(a)
	must(t, err)
	if id.Birth == 0 {
		t.Skip("filesystem has no birth time")
	}
	e := &Entry{Path: a, Dev: id.Dev, Ino: id.Ino, Hi: id.Hi, Birth: id.Birth}
	dst := filepath.Join(s.work, ".config", "tool", "renamed")
	must(t, os.Rename(a, dst))
	if got := scanResolve(e, 2*time.Second); got != dst {
		t.Errorf("scanResolve = %q, want %q", got, dst)
	}
	hidden := filepath.Join(s.folder(".cache"), "renamed") // some other hidden folder: not searched
	must(t, os.Rename(dst, hidden))
	if got := scanResolve(e, 2*time.Second); got != "" {
		t.Errorf("searched an unrelated hidden folder: %q", got)
	}
}

func TestInstallIdempotentAndUninstall(t *testing.T) {
	s := newSandbox(t)
	sp := filepath.Join(s.home, ".claude", "settings.json")
	orig := `{"model":"opus","hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo hi && true"}]}]}}`
	must(t, os.WriteFile(sp, []byte(orig), 0o644))
	for i := 0; i < 2; i++ {
		if code, out, errs := s.run("", "install"); code != 0 {
			t.Fatalf("install: %s %s", out, errs)
		}
	}
	var st struct {
		Hooks struct {
			SessionStart []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"SessionStart"`
		} `json:"hooks"`
	}
	b, _ := os.ReadFile(sp)
	must(t, json.Unmarshal(b, &st))
	g := st.Hooks.SessionStart
	if len(g) != 2 || g[0].Hooks[0].Command != "echo hi && true" || !strings.Contains(g[1].Hooks[0].Command, "intow/bin/intow"+exeSuffix) || !strings.HasSuffix(g[1].Hooks[0].Command, " hook") {
		t.Fatalf("settings after install:\n%s", b)
	}
	if !strings.Contains(string(b), "echo hi && true") {
		t.Error("other hook was re-escaped")
	}
	if fi, err := os.Stat(installedBin()); err != nil || (fi.Mode()&0o111 == 0 && runtime.GOOS != "windows") {
		t.Error("binary not installed")
	}
	s.run("", "uninstall")
	b, _ = os.ReadFile(sp)
	var after, before any
	json.Unmarshal(b, &after)
	json.Unmarshal([]byte(orig), &before)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("uninstall left:\n%s", b)
	}
}

func TestOnlyOurHookIsOurs(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/home/u/.claude/intow/bin/intow hook":                   true,
		"'/home/my user/.claude/intow/bin/intow' hook":           true,
		`"C:/Users/John Smith/.claude/intow/bin/intow.exe" hook`: true,
		"intow hook":                           true,
		"/opt/maintown/bin/notify hook":        false, // "intow" inside another word
		"/home/u/.claude/intow/bin/other hook": false,
		"/usr/bin/intow status":                false,
		"echo intow hook && true":              false,
	} {
		if got := isOurHook(cmd); got != want {
			t.Errorf("isOurHook(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestForget(t *testing.T) {
	s := newSandbox(t)
	s.setupProject()
	_, out, _ := s.run("", "forget", s.work)
	if !strings.Contains(out, "forgot 2") || len(loadRegistry().Entries) != 0 {
		t.Errorf("forget: %s", out)
	}
}

func TestParseArgsAnywhere(t *testing.T) {
	o, err := parseArgs([]string{"a", "-n", "b", "--bin-dir=x"}, []string{"n"}, []string{"bin-dir"})
	if err != nil || !o.has("n") || o.vals["bin-dir"] != "x" || !reflect.DeepEqual(o.args, []string{"a", "b"}) {
		t.Errorf("%+v %v", o, err)
	}
}

// ---- Claude's own files: symlinks, locking, other path-keyed state

func (s *sandbox) symlinked(name, content string) (link, real string) {
	real = filepath.Join(s.root, "dotfiles", name)
	must(s.t, os.MkdirAll(filepath.Dir(real), 0o755))
	must(s.t, os.WriteFile(real, []byte(content), 0o600))
	link = filepath.Join(s.home, name)
	if !strings.HasPrefix(name, ".claude.json") {
		link = filepath.Join(s.home, ".claude", name)
	}
	os.Remove(link)
	needSymlink(s.t, real, link)
	return link, real
}

// needSymlink makes link point at target, or skips the test where that is not
// possible: Windows without the privilege, or Wine, which follows links but
// does not show them as links.
func needSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here: ", err)
	}
	if !isSymlink(link) {
		os.Remove(link)
		t.Skip("symlinks are not visible as such here")
	}
}

func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func TestInstallKeepsSymlinkedSettings(t *testing.T) {
	s := newSandbox(t)
	link, real := s.symlinked("settings.json", `{"model":"opus"}`)
	if code, out, errs := s.run("", "install"); code != 0 {
		t.Fatalf("install: %s %s", out, errs)
	}
	b, _ := os.ReadFile(real)
	if !isSymlink(link) || !strings.Contains(string(b), "intow") || !strings.Contains(string(b), `"model"`) {
		t.Errorf("symlink kept: %v, target:\n%s", isSymlink(link), b)
	}
	s.run("", "uninstall")
	if b, _ := os.ReadFile(real); !isSymlink(link) || strings.Contains(string(b), "intow") {
		t.Errorf("after uninstall, symlink kept: %v, target:\n%s", isSymlink(link), b)
	}
}

func TestInstallThroughDanglingRelativeSymlink(t *testing.T) {
	s := newSandbox(t)
	link := filepath.Join(s.home, ".claude", "settings.json")
	must(t, os.MkdirAll(filepath.Join(s.home, "dotfiles"), 0o755))
	needSymlink(t, filepath.Join("..", "dotfiles", "settings.json"), link)
	if code, out, errs := s.run("", "install"); code != 0 {
		t.Fatalf("install: %s %s", out, errs)
	}
	b, err := os.ReadFile(filepath.Join(s.home, "dotfiles", "settings.json"))
	if err != nil || !isSymlink(link) || !strings.Contains(string(b), "intow") {
		t.Errorf("symlink kept: %v, target: %s %v", isSymlink(link), b, err)
	}
}

func TestSymlinkedHistoryAndGlobalJSON(t *testing.T) {
	s := newSandbox(t)
	old, newp := filepath.Join(s.work, "gone"), s.folder("here")
	s.session(old, "o", nil)
	hl, hreal := s.symlinked("history.jsonl", `{"display":"hi","project":"`+q(old)+`"}`+"\n"+`{"display":"x","project":"`+q(sub(old, "sub"))+`"}`+"\n")
	gl, greal := s.symlinked(".claude.json", `{"projects":{"`+q(old)+`":{"allowedTools":[]}}}`)
	if code, out, errs := s.run("", "link", old, newp); code != 0 || !strings.Contains(out, "history.jsonl: 2 line(s)") {
		t.Fatalf("link: %s %s", out, errs)
	}
	h, _ := os.ReadFile(hreal)
	g, _ := os.ReadFile(greal)
	if !isSymlink(hl) || strings.Contains(string(h), q(old)) || !strings.Contains(string(h), q(sub(newp, "sub"))+`"`) {
		t.Errorf("history symlink kept: %v, target:\n%s", isSymlink(hl), h)
	}
	if !isSymlink(gl) || strings.Contains(string(g), q(old)) || !strings.Contains(string(g), q(newp)) {
		t.Errorf("config symlink kept: %v, target:\n%s", isSymlink(gl), g)
	}
}

func TestRepoPathsAndPluginInstallsFollow(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	gj := s.gj()
	gj["githubRepoPaths"] = map[string]any{"o/app": []any{proj, "/elsewhere/app"}, "o/sub": []any{sub(proj, "sub")}, "o/other": []any{"/x"}}
	s.writeGJ(gj)
	pfile := filepath.Join(s.home, ".claude", "plugins", "installed_plugins.json")
	must(t, os.MkdirAll(filepath.Dir(pfile), 0o755))
	plugins := "{\n  \"version\": 2,\n  \"plugins\": {\n    \"a@m\": [\n      {\n        \"scope\": \"project\",\n        \"projectPath\": \"" + q(proj) +
		"\",\n        \"installPath\": \"/cache/a\"\n      },\n      {\n        \"scope\": \"user\",\n        \"installPath\": \"/cache/a\"\n      }\n    ],\n    \"b@m\": [\n      {\n        \"scope\": \"local\",\n        \"projectPath\": \"/unrelated\"\n      }\n    ]\n  }\n}\n"
	must(t, os.WriteFile(pfile, []byte(plugins), 0o600))
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	_, out, _ := s.run("", "sync")
	if !strings.Contains(out, "githubRepoPaths: 2 path(s)") || !strings.Contains(out, "installed_plugins.json: 1 install(s)") {
		t.Fatalf("dry run should count them:\n%s", out)
	}
	if b, _ := os.ReadFile(pfile); string(b) != plugins {
		t.Fatal("dry run changed installed_plugins.json")
	}
	if code, out, errs := s.run("", "sync", "--apply"); code != 0 {
		t.Fatalf("sync: %s %s", out, errs)
	}
	want := map[string]any{"o/app": []any{newp, "/elsewhere/app"}, "o/sub": []any{sub(newp, "sub")}, "o/other": []any{"/x"}}
	if got := s.gj()["githubRepoPaths"]; !reflect.DeepEqual(got, want) {
		t.Errorf("githubRepoPaths: %v", got)
	}
	b, _ := os.ReadFile(pfile)
	if string(b) != strings.Replace(plugins, q(proj), q(newp), 1) {
		t.Errorf("installed_plugins.json:\n%s", b)
	}
}

// Claude Code holds "<file>.lock" (a directory) while it writes ~/.claude.json.
func TestGlobalJSONUnderClaudeLockAndConcurrentWrite(t *testing.T) {
	s := newSandbox(t)
	old, newp := filepath.Join(s.work, "gone"), s.folder("here")
	s.session(old, "o", nil)
	gjPath := filepath.Join(s.home, ".claude.json")
	s.writeGJ(map[string]any{"numStartups": 1, "projects": map[string]any{old: map[string]any{"allowedTools": []any{"Bash(ls)"}}}})
	wrote := 0
	beforeClaudeWrite = func(path string) {
		if path != gjPath {
			return
		}
		if !isDir(gjPath + ".lock") {
			t.Error("not holding Claude's lock while editing")
		}
		if wrote++; wrote == 1 { // Claude saves between our read and our write
			gj := s.gj()
			gj["numStartups"] = 2
			gj["projects"].(map[string]any)["/added/by/claude"] = map[string]any{}
			s.writeGJ(gj)
		}
	}
	t.Cleanup(func() { beforeClaudeWrite = nil })
	if code, out, errs := s.run("", "link", old, newp); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	gj, pj := s.gj(), s.gjProjects()
	if gj["numStartups"] != float64(2) || pj["/added/by/claude"] == nil || pj[newp] == nil || pj[old] != nil || wrote != 2 {
		t.Errorf("Claude's write was lost or the entry did not move (attempts %d): %v", wrote, gj)
	}
	if exists(gjPath + ".lock") {
		t.Error("lock left behind")
	}
}

func TestProceedsWhenClaudeLockStaysBusy(t *testing.T) {
	s := newSandbox(t)
	old, newp := filepath.Join(s.work, "gone"), s.folder("here")
	s.session(old, "o", nil)
	s.writeGJ(map[string]any{"projects": map[string]any{old: map[string]any{}}})
	lock := filepath.Join(s.home, ".claude.json.lock")
	must(t, os.Mkdir(lock, 0o755)) // e.g. left by a Claude process that was killed
	saved := claudeLockWait
	claudeLockWait = 60 * time.Millisecond
	t.Cleanup(func() { claudeLockWait = saved })
	if code, out, errs := s.run("", "link", old, newp); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	if s.gjProjects()[newp] == nil || !isDir(lock) {
		t.Errorf("entry moved: %v, foreign lock kept: %v", s.gjProjects()[newp] != nil, isDir(lock))
	}
}

// ---- Claude Code changing how it names or writes things

func TestNamingDriftPausesFollowing(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	// Claude Code now keeps this folder's sessions under a name encode does not produce.
	odd := filepath.Join(s.home, ".claude", "projects", "v2-"+filepath.Base(newp), "current.jsonl")
	must(t, os.MkdirAll(filepath.Dir(odd), 0o755))
	must(t, os.WriteFile(filepath.Join(filepath.Dir(odd), "earlier.jsonl"), []byte(`{"type":"user","cwd":"`+q(newp)+`"}`+"\n"), 0o600))
	out := s.hookAt(newp, odd)
	if out == nil || !strings.Contains(out["systemMessage"].(string), "paused") || !exists(s.pdir(proj)) {
		t.Fatalf("expected a pause and no migration: %v", out)
	}
	if code, _, errs := s.run("", "sync", "--apply"); code == 0 || !strings.Contains(errs, "paused") {
		t.Errorf("sync --apply while paused: exit %d, %s", code, errs)
	}
	if code, out, errs := s.run("", "sync", "--apply", "-q"); code != 0 || out+errs != "" {
		t.Errorf("quiet sync while paused should say nothing: %d %q %q", code, out, errs)
	}
	if code, _, errs := s.run("", "link", proj, newp); code == 0 || !strings.Contains(errs, "paused") {
		t.Errorf("link while paused: exit %d, %s", code, errs)
	}
	if _, out, _ := s.run("", "status"); !strings.Contains(out, "paused") {
		t.Errorf("status: %s", out)
	}
	// A session whose folder is named as expected lifts the pause.
	out = s.hook(newp)
	if out == nil || strings.Contains(out["systemMessage"].(string), "paused") || exists(s.pdir(proj)) || !isFile(filepath.Join(s.pdir(newp), "s1.jsonl")) {
		t.Fatalf("expected the migration after the names agree again: %v", out)
	}
}

// A session resumed from another git worktree keeps its transcript in that
// worktree's project folder: not a new naming scheme, and not where this
// folder's history belongs.
func TestSessionInAnotherFoldersProjectDir(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	other := s.folder("worktree-b")
	s.session(other, "w1", nil)
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	out := s.hookAt(newp, filepath.Join(s.pdir(other), "w1.jsonl"))
	if out == nil || strings.Contains(out["systemMessage"].(string), "paused") || loadRegistry().Drift != nil {
		t.Fatalf("must not pause: %v", out)
	}
	if !isFile(filepath.Join(s.pdir(newp), "s1.jsonl")) || isFile(filepath.Join(s.pdir(other), "s1.jsonl")) {
		t.Errorf("history must land in the renamed folder's own project dir: %v / %v", listDir(t, s.pdir(other)), exists(s.pdir(newp)))
	}
}

func TestFixedProjectDirNameIsLeftAlone(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "pinned")
	if out := s.hookAt(newp, filepath.Join(s.home, ".claude", "projects", "pinned", "current.jsonl")); out != nil || !exists(s.pdir(proj)) {
		t.Errorf("hook should do nothing: %v", out)
	}
	if loadRegistry().Drift != nil {
		t.Error("a pinned name is not drift")
	}
}

func TestSpacedCwdAndUnknownTranscriptFormat(t *testing.T) {
	s := newSandbox(t)
	old, newp := filepath.Join(s.work, "gone"), s.folder("here")
	must(t, os.MkdirAll(s.pdir(old), 0o755))
	spaced := filepath.Join(s.pdir(old), "a.jsonl")
	must(t, os.WriteFile(spaced, []byte(`{"type": "user", "cwd": "`+q(old)+`"}`+"\n"), 0o600))
	if code, out, errs := s.run("", "link", old, newp); code != 0 || strings.Contains(out, "unknown transcript format") {
		t.Fatalf("link: %s %s", out, errs)
	}
	if got := cwds(t, filepath.Join(s.pdir(newp), "a.jsonl"))[0]; got != newp {
		t.Errorf("spaced cwd = %v", got)
	}
	old2, new2 := filepath.Join(s.work, "gone2"), s.folder("here2")
	must(t, os.MkdirAll(s.pdir(old2), 0o755))
	must(t, os.WriteFile(filepath.Join(s.pdir(old2), "b.jsonl"), []byte(`{"type":"user","cwd" : "`+q(old2)+`"}`+"\n"), 0o600))
	if _, out, _ := s.run("", "link", old2, new2); !strings.Contains(out, "unknown transcript format") {
		t.Errorf("expected a note about the format:\n%s", out)
	}
}

// ---- a path reused by another folder

func TestReusedPathKeepsMovedFolder(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	if id, err := statIdent(proj); err != nil || (scanFallback && id.Birth == 0) {
		t.Skip("filesystem has no birth time")
	}
	aside := filepath.Join(s.work, "proj-old")
	must(t, os.Rename(proj, aside))
	must(t, os.MkdirAll(proj, 0o755)) // a fresh folder under the old name
	// Claude starts in the fresh folder first: the history under this path is
	// not its own, but whose it is cannot be known, so it stays. The subfolder
	// is unambiguous (its old path is gone) and is followed as usual.
	s.hook(proj)
	if !isFile(filepath.Join(s.pdir(proj), "s1.jsonl")) || !isFile(filepath.Join(s.pdir(sub(aside, "sub")), "s3.jsonl")) {
		t.Fatal("the folder's history must stay, its subfolder's must follow")
	}
	hint := "intow link --force " + shellQuote(proj) + " " + shellQuote(aside)
	if _, out, _ := s.run("", "status"); !strings.Contains(out, hint) {
		t.Fatalf("status should offer the history to the moved folder:\n%s", out)
	}
	// Later Claude starts in the folder that was moved aside.
	out := s.hook(aside)
	if out == nil || !strings.Contains(out["systemMessage"].(string), hint) {
		t.Fatalf("hook: %v", out)
	}
	if _, out, _ := s.run("", "status"); !strings.Contains(out, hint) {
		t.Fatalf("status should still offer it:\n%s", out)
	}
	if code, out, errs := s.run("", "link", "--force", proj, aside); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	if !isFile(filepath.Join(s.pdir(aside), "s1.jsonl")) || !isFile(filepath.Join(s.pdir(sub(aside, "sub")), "s3.jsonl")) {
		t.Error("history not attached to the moved folder")
	}
	paths := map[string]int{}
	for _, e := range loadRegistry().Entries {
		paths[e.Path]++
		if e.Was != "" {
			t.Errorf("hint not cleared for %s", e.Path)
		}
	}
	if paths[proj] != 1 || paths[aside] != 1 {
		t.Errorf("registry should track both folders where they are: %v", paths)
	}
	if _, out, _ := s.run("", "status"); strings.Contains(out, "link --force") {
		t.Errorf("status still offers:\n%s", out)
	}
}

// Profiles: several config dirs share one projects folder through a symlink,
// each with its own ~/.claude.json and registry. The first to start follows a
// moved folder; the others only catch up on their own settings, share the
// lock, and do not offer history that has been moved already.
func TestConfigDirsSharingProjects(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	main := filepath.Join(s.home, ".claude")
	prof := filepath.Join(s.root, "profile")
	must(t, os.MkdirAll(prof, 0o755))
	needSymlink(t, filepath.Join(main, "projects"), filepath.Join(prof, "projects"))
	gj, err := os.ReadFile(filepath.Join(s.home, ".claude.json"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(prof, ".claude.json"), gj, 0o600))
	inProfile := func(f func()) {
		t.Setenv("CLAUDE_CONFIG_DIR", prof)
		defer t.Setenv("CLAUDE_CONFIG_DIR", "")
		f()
	}
	inProfile(func() {
		s.run("", "scan", "-q")
		if want := filepath.Join(main, "intow", "lock"); lockPath() != want {
			t.Errorf("lock = %s, want the shared %s", lockPath(), want)
		}
	})

	// The folder is moved aside and a new one takes its place.
	aside := filepath.Join(s.work, "proj-old")
	must(t, os.Rename(proj, aside))
	if id, err := statIdent(aside); err != nil || (scanFallback && id.Birth == 0) {
		t.Skip("filesystem has no birth time")
	}
	// The main config dir sees it first, before the new folder exists.
	if out := s.hook(aside); out == nil || !isFile(filepath.Join(s.pdir(aside), "s1.jsonl")) {
		t.Fatalf("main config dir did not follow: %v", out)
	}
	must(t, os.MkdirAll(proj, 0o755))
	s.session(proj, "fresh", nil) // the new folder's own history, under the old path

	inProfile(func() {
		in, _ := json.Marshal(map[string]string{"session_id": "current", "cwd": aside,
			"transcript_path": filepath.Join(prof, "projects", encode(aside), "current.jsonl")})
		_, out, _ := s.run(string(in), "hook")
		if strings.Contains(out, "link --force") {
			t.Errorf("the profile offers history that was moved already: %s", out)
		}
		var pj struct {
			Projects map[string]any `json:"projects"`
		}
		b, _ := os.ReadFile(filepath.Join(prof, ".claude.json"))
		must(t, json.Unmarshal(b, &pj))
		// The moved folder gets the settings; the folder now at the old path keeps them too.
		if e, _ := pj.Projects[aside].(map[string]any); e == nil || e["hasTrustDialogAccepted"] != true || pj.Projects[proj] == nil {
			t.Errorf("the profile's own settings did not follow: %v", pj.Projects)
		}
		if _, out, _ := s.run("", "status"); strings.Contains(out, "link --force") {
			t.Errorf("status in the profile offers it: %s", out)
		}
	})
	if !isFile(filepath.Join(s.pdir(proj), "fresh.jsonl")) || !isFile(filepath.Join(s.pdir(aside), "s1.jsonl")) {
		t.Error("histories got mixed up")
	}
}

func TestReusedPathForgetsDeletedFolder(t *testing.T) {
	s := newSandbox(t)
	proj := s.folder("solo")
	s.session(proj, "s1", nil)
	s.run("", "scan", "-q")
	must(t, os.RemoveAll(proj))
	must(t, os.MkdirAll(proj, 0o755))
	s.hook(proj)
	if n := len(loadRegistry().Entries); n != 1 {
		t.Errorf("registry has %d entries, want only the new folder", n)
	}
}

// ---- one folder under two names

// On a case-insensitive volume (the macOS default) the project dirs of "app"
// and "App" are one directory. Merging one into the other would delete it.
func TestSameDirectoryUnderTwoNamesIsNeverMerged(t *testing.T) {
	s := newSandbox(t)
	old, newp := filepath.Join(s.work, "app"), s.folder("renamed")
	s.session(old, "s1", map[string]string{"MEMORY.md": "- [Goal](goal.md)\n", "goal.md": "g\n"})
	// Stand-in for such a volume: the new name leads to the same directory.
	needSymlink(t, s.pdir(old), s.pdir(newp))
	if code, out, errs := s.run("", "link", "--force", old, newp); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	for _, f := range []string{"s1.jsonl", "memory/MEMORY.md", "memory/goal.md"} {
		if !isRegular(filepath.Join(s.pdir(old), filepath.FromSlash(f))) {
			t.Errorf("%s was deleted", f)
		}
	}
}

func caseInsensitive(t *testing.T, dir string) bool {
	probe := filepath.Join(dir, "CaseProbe")
	must(t, os.WriteFile(probe, nil, 0o600))
	defer os.Remove(probe)
	return exists(filepath.Join(dir, "caseprobe"))
}

// The real thing, where the filesystem allows it (macOS and Windows CI).
func TestCaseOnlyRename(t *testing.T) {
	s := newSandbox(t)
	if !caseInsensitive(t, s.root) {
		t.Skip("this filesystem is case-sensitive")
	}
	proj := s.folder("app")
	s.session(proj, "s1", map[string]string{"MEMORY.md": "- [Goal](goal.md)\n", "goal.md": "g\n"})
	s.writeGJ(map[string]any{"projects": map[string]any{proj: map[string]any{"hasTrustDialogAccepted": true}}})
	s.run("", "scan", "-q")
	newp := filepath.Join(s.work, "App")
	must(t, os.Rename(proj, newp))
	out := s.hook(newp)
	if !caseRename(proj, newp) {
		if out != nil {
			t.Errorf("two spellings of one folder are both valid here; the hook should stay quiet: %v", out)
		}
	} else if out == nil || strings.Contains(out["systemMessage"].(string), "another folder") {
		t.Fatalf("a case-only rename is a rename: %v", out)
	} else {
		if got := cwds(t, filepath.Join(s.pdir(newp), "s1.jsonl"))[0]; got != newp {
			t.Errorf("cwd = %v", got)
		}
		if _, ok := s.gjProjects()[newp]; !ok {
			t.Errorf("settings not re-keyed: %v", s.gjProjects())
		}
	}
	// Whatever the platform decided, and whatever the user then forces: nothing is lost.
	s.run("", "link", "--force", proj, newp)
	for _, f := range []string{"s1.jsonl", "memory/MEMORY.md", "memory/goal.md"} {
		if !isRegular(filepath.Join(s.pdir(newp), filepath.FromSlash(f))) {
			t.Errorf("%s was deleted", f)
		}
	}
}

// ---- undo, shell-init

func (s *sandbox) history(lines ...string) string {
	p := filepath.Join(s.home, ".claude", "history.jsonl")
	must(s.t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return p
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	must(t, err)
	return string(b)
}

// A rename-mode migration (Claude had not started in the new place yet) is
// taken back completely, byte for byte.
func TestUndo(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	hist := s.history(`{"display":"a","project":"`+q(proj)+`"}`, `{"display":"b","project":"/other"}`, `{"display":"c","project":"`+q(sub(proj, "sub"))+`"}`)
	s1 := filepath.Join(s.pdir(proj), "s1.jsonl")
	before := map[string]string{s1: readFile(t, s1), hist: readFile(t, hist)}
	gjBefore := s.gj()
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	if code, out, errs := s.run("", "sync", "--apply"); code != 0 || !isFile(filepath.Join(s.pdir(newp), "s1.jsonl")) {
		t.Fatalf("sync: %s %s", out, errs)
	}
	_, out, _ := s.run("", "undo", "-n")
	if !strings.Contains(out, "would undo") || !strings.Contains(out, "3 sessions") || !strings.Contains(out, "2 history lines") || !exists(s.pdir(newp)) {
		t.Fatalf("dry run:\n%s", out)
	}
	if code, out, errs := s.run("", "undo"); code != 0 || !strings.Contains(out, "3 sessions") {
		t.Fatalf("undo: %s %s", out, errs)
	}
	if exists(s.pdir(newp)) || exists(s.pdir(sub(newp, "sub"))) || !isFile(filepath.Join(s.pdir(sub(proj, "sub")), "s3.jsonl")) {
		t.Errorf("data not back under the old path: %v", listDir(t, filepath.Join(s.home, ".claude", "projects")))
	}
	for p, want := range before {
		if got := readFile(t, p); got != want {
			t.Errorf("%s not restored:\n%s", filepath.Base(p), got)
		}
	}
	if fi, _ := os.Stat(s1); fi.ModTime().Unix() != oldMtime {
		t.Error("mtime not kept")
	}
	if !isFile(filepath.Join(s.pdir(proj), "memory", "goal.md")) {
		t.Error("memory not back")
	}
	if !reflect.DeepEqual(s.gj(), gjBefore) {
		t.Errorf("settings not restored: %v", s.gj())
	}
	if len(loadRegistry().Entries) != 0 {
		t.Error("the folder should not be tracked after undo")
	}
	if out := s.hook(newp); out != nil || !exists(s.pdir(proj)) {
		t.Errorf("the folder must not be followed again: %v", out)
	}
	if code, out, _ := s.run("", "undo"); code != 1 || !strings.Contains(out, "no migration to undo") {
		t.Errorf("a second undo has nothing to do: %d %s", code, out)
	}
	if _, out, _ := s.run("", "log"); !strings.Contains(out, "[undo]") {
		t.Errorf("log should show the undo:\n%s", out)
	}
}

// The hook usually merges: Claude has already created the project dir of the
// new path. Undo takes back what the migration brought and leaves what Claude
// has written there since.
func TestUndoAfterMergeKeepsWhatIsNew(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	// Claude already works in the new place: a session, a memory index, settings.
	s.session(newp, "n1", map[string]string{"MEMORY.md": "- [New](new.md) — made here\n", "new.md": "n\n"})
	gj := s.gj()
	gj["projects"].(map[string]any)[newp] = map[string]any{"allowedTools": []any{"Read"}, "hasTrustDialogAccepted": false}
	s.writeGJ(gj)
	hadEntry := s.gjProjects()[newp]
	if out := s.hook(newp); out == nil {
		t.Fatal("hook did nothing")
	}
	mem := filepath.Join(s.pdir(newp), "memory")
	if got := listDir(t, s.pdir(newp)); !reflect.DeepEqual(got, []string{"memory", "n1.jsonl", "s1.jsonl", "s2.jsonl"}) {
		t.Fatalf("merged project dir: %v", got)
	}
	if idx := readFile(t, filepath.Join(mem, "MEMORY.md")); !strings.Contains(idx, "[Goal](goal.md)") || !strings.Contains(idx, "[New](new.md)") {
		t.Fatalf("merged index:\n%s", idx)
	}
	// Since then Claude continued one of the old sessions here and started another.
	cont := filepath.Join(s.pdir(newp), "s2.jsonl")
	f, err := os.OpenFile(cont, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	f.WriteString(`{"type":"user","cwd":"` + q(newp) + `"}` + "\n")
	must(t, f.Close())
	s.session(newp, "n2", nil)

	if code, out, errs := s.run("", "undo", newp); code != 0 || !strings.Contains(out, "1 session continued") {
		t.Fatalf("undo: %s %s", out, errs)
	}
	if got := listDir(t, s.pdir(newp)); !reflect.DeepEqual(got, []string{"memory", "n1.jsonl", "n2.jsonl", "s2.jsonl"}) {
		t.Errorf("new project dir after undo: %v", got)
	}
	if got := listDir(t, s.pdir(proj)); !reflect.DeepEqual(got, []string{"memory", "s1.jsonl"}) {
		t.Errorf("old project dir after undo: %v", got)
	}
	if got := cwds(t, filepath.Join(s.pdir(proj), "s1.jsonl"))[0]; got != proj {
		t.Errorf("cwd of the session taken back = %v", got)
	}
	if got := cwds(t, cont)[0]; got != newp {
		t.Errorf("cwd of the continued session = %v", got)
	}
	if got := readFile(t, filepath.Join(mem, "MEMORY.md")); got != "- [New](new.md) — made here\n" {
		t.Errorf("new index after undo:\n%s", got)
	}
	if got := readFile(t, filepath.Join(s.pdir(proj), "memory", "MEMORY.md")); got != "- [Goal](goal.md) — the goal\n" {
		t.Errorf("old index after undo:\n%s", got)
	}
	if !isFile(filepath.Join(s.pdir(proj), "memory", "goal.md")) || !isFile(filepath.Join(mem, "new.md")) || exists(filepath.Join(mem, "goal.md")) {
		t.Error("memory files not split back")
	}
	pj := s.gjProjects()
	if !reflect.DeepEqual(pj[newp], hadEntry) {
		t.Errorf("settings of the new path should be what they were before: %v", pj[newp])
	}
	if e, _ := pj[proj].(map[string]any); e == nil || e["hasTrustDialogAccepted"] != true {
		t.Errorf("settings of the old path not back: %v", pj[proj])
	}
	if !isFile(filepath.Join(s.pdir(sub(proj, "sub")), "s3.jsonl")) {
		t.Error("subfolder session not back")
	}
}

func TestUndoLeavesChangedSettingsAndHistory(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	hist := s.history(`{"display":"a","project":"` + q(proj) + `"}`)
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	s.run("", "sync", "--apply")
	// Claude ran in the new place: it touched the entry and added a history line.
	gj := s.gj()
	gj["projects"].(map[string]any)[newp].(map[string]any)["lastCost"] = 1.5
	s.writeGJ(gj)
	f, err := os.OpenFile(hist, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	f.WriteString(`{"display":"later","project":"` + q(newp) + `"}` + "\n")
	must(t, f.Close())
	if code, out, errs := s.run("", "undo"); code != 0 || !strings.Contains(out, "changed since") {
		t.Fatalf("undo: %s %s", out, errs)
	}
	pj := s.gjProjects()
	if e, _ := pj[newp].(map[string]any); e == nil || e["lastCost"] != 1.5 {
		t.Errorf("the changed entry must stay: %v", pj[newp])
	}
	if e, _ := pj[proj].(map[string]any); e == nil || e["hasTrustDialogAccepted"] != true {
		t.Errorf("the old path must have its entry back: %v", pj[proj])
	}
	want := `{"display":"a","project":"` + q(proj) + `"}` + "\n" + `{"display":"later","project":"` + q(newp) + `"}` + "\n"
	if got := readFile(t, hist); got != want {
		t.Errorf("history:\n%s", got)
	}
}

func TestUndoGoesBackOneMigrationAtATime(t *testing.T) {
	s := newSandbox(t)
	a := s.folder("a")
	s.session(a, "s1", nil)
	s.run("", "scan", "-q")
	b, c := filepath.Join(s.work, "b"), filepath.Join(s.work, "c")
	must(t, os.Rename(a, b))
	s.run("", "sync", "--apply")
	s.run("", "scan", "-q")
	must(t, os.Rename(b, c))
	s.run("", "sync", "--apply")
	if !isFile(filepath.Join(s.pdir(c), "s1.jsonl")) {
		t.Fatal("not at c")
	}
	if code, out, _ := s.run("", "undo", a); code != 0 || !strings.Contains(out, a+" -> "+b) {
		t.Fatalf("undo of a named folder picks its migration: %s", out) // nothing to take back: the data is at c
	}
	if !isFile(filepath.Join(s.pdir(c), "s1.jsonl")) {
		t.Fatal("undoing a -> b must not touch what has moved on to c")
	}
	if code, out, errs := s.run("", "undo"); code != 0 || !isFile(filepath.Join(s.pdir(b), "s1.jsonl")) {
		t.Fatalf("undo c -> b: %s %s", out, errs)
	}
	if got := cwds(t, filepath.Join(s.pdir(b), "s1.jsonl"))[0]; got != b {
		t.Errorf("cwd = %v", got)
	}
}

func TestUndoRefusesWhileClaudeRunsThere(t *testing.T) {
	s := newSandbox(t)
	proj := s.setupProject()
	newp := filepath.Join(s.work, "renamed")
	must(t, os.Rename(proj, newp))
	s.run("", "sync", "--apply")
	sdir := filepath.Join(s.home, ".claude", "sessions")
	must(t, os.MkdirAll(sdir, 0o755))
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "zzz", "cwd": newp})
	must(t, os.WriteFile(filepath.Join(sdir, fmt.Sprintf("%d.json", os.Getpid())), b, 0o644))
	if code, _, errs := s.run("", "undo"); code == 0 || !strings.Contains(errs, "is running") || !exists(s.pdir(newp)) {
		t.Errorf("undo must wait for Claude to quit: %d %s", code, errs)
	}
	if code, out, errs := s.run("", "undo", "--force"); code != 0 || exists(s.pdir(newp)) {
		t.Errorf("undo --force: %s %s", out, errs)
	}
}

func TestShellInit(t *testing.T) {
	s := newSandbox(t)
	for shell, want := range map[string]string{"zsh": `command claude "$@"`, "bash": `command claude "$@"`, "fish": "command claude $argv"} {
		code, out, _ := s.run("", "shell-init", shell)
		if code != 0 || !strings.Contains(out, want) || !strings.Contains(out, "sync --apply -q") {
			t.Errorf("%s: %d\n%s", shell, code, out)
		}
	}
	if code, out, _ := s.run("", "shell-init", "powershell"); code != 0 || !strings.Contains(out, "function claude {") ||
		!strings.Contains(out, "sync --apply -q") || !strings.Contains(out, "-CommandType Application") {
		t.Errorf("powershell: %d\n%s", code, out)
	}
	if code, _, _ := s.run("", "shell-init", "tcsh"); code != 2 {
		t.Errorf("unknown shell: exit %d", code)
	}
}

// ---- plugin launcher

func TestPluginLauncherPinsChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script")
	}
	for _, tool := range []string{"sh", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	plug, data, rel, build := filepath.Join(root, "plugin"), filepath.Join(root, "data"), filepath.Join(root, "release", "v9.9.9"), filepath.Join(root, "build")
	for _, d := range []string{filepath.Join(plug, ".claude-plugin"), filepath.Join(plug, "scripts"), rel, build} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.WriteFile(filepath.Join(plug, ".claude-plugin", "plugin.json"), []byte("{\n  \"name\": \"intow\",\n  \"version\": \"9.9.9\"\n}\n"), 0o644))
	script, err := os.ReadFile(filepath.Join("plugin", "scripts", "intow"))
	must(t, err)
	launcher := filepath.Join(plug, "scripts", "intow")
	must(t, os.WriteFile(launcher, script, 0o755))
	must(t, os.WriteFile(filepath.Join(build, "intow"), []byte("#!/bin/sh\necho ran \"$@\"\n"), 0o755))
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	asset := "intow_9.9.9_" + runtime.GOOS + "_" + arch + ".tar.gz"
	if out, err := exec.Command("tar", "-czf", filepath.Join(rel, asset), "-C", build, "intow").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}
	blob, err := os.ReadFile(filepath.Join(rel, asset))
	must(t, err)
	good := fmt.Sprintf("%x  %s\n", sha256.Sum256(blob), asset)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{launcher}, args...)...)
		cmd.Env = append(os.Environ(), "CLAUDE_PLUGIN_ROOT="+plug, "CLAUDE_PLUGIN_DATA="+data,
			"INTOW_RELEASE_BASE=file://"+filepath.Join(root, "release"), "INTOW_BIN=", "INTOW_CHECKSUMS=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	sums := filepath.Join(plug, "checksums.txt")

	if out, err := run("status"); err == nil || !strings.Contains(out, "no checksum") {
		t.Errorf("without a pinned checksum: %v %s", err, out)
	}
	if out, err := run("hook"); err != nil || out != "" {
		t.Errorf("as a hook it must stay silent and succeed: %v %q", err, out)
	}
	must(t, os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  "+asset+"\n"), 0o644))
	if out, err := run("status"); err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Errorf("with the wrong checksum: %v %s", err, out)
	}
	if exists(filepath.Join(data, "bin")) {
		t.Error("an unverified binary was installed")
	}
	must(t, os.WriteFile(sums, []byte(good), 0o644))
	if out, err := run("status", "--json"); err != nil || strings.TrimSpace(out) != "ran status --json" {
		t.Fatalf("with the right checksum: %v %q", err, out)
	}
	if got, err := os.Readlink(filepath.Join(data, "bin", "intow")); err != nil || got != "intow-9.9.9" {
		t.Errorf("stable name: %q %v", got, err)
	}
}
