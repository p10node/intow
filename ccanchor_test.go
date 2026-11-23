package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- encoding

func TestEncodeKnown(t *testing.T) {
	cases := map[string]string{
		"/Users/p10node/WD/workplace--p10node/misc-research": "-Users-p10node-WD-workplace--p10node-misc-research",
		"/private/var/folders/p_/3g670bz50jj4y67g7_hzrg1h0000gn/T":   "-private-var-folders-p--3g670bz50jj4y67g7-hzrg1h0000gn-T",
		"/" + strings.Repeat("z", 199):                               "-" + strings.Repeat("z", 199),
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
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", "")
	saved := tempPrefixes
	tempPrefixes = nil // the sandbox itself lives in a temp dir
	t.Cleanup(func() { tempPrefixes = saved })
	s.writeGJ(map[string]any{"projects": map[string]any{}})
	return s
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
		{"type": "assistant", "cwd": cwd + "/subdir", "sessionId": sid},
		{"type": "tool", "toolUseResult": `{"cwd":"` + cwd + `"}`},
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
	in, _ := json.Marshal(map[string]string{
		"session_id": "current", "cwd": cwd, "hook_event_name": "SessionStart",
		"transcript_path": filepath.Join(s.pdir(cwd), "current.jsonl"),
	})
	code, out, _ := s.run(string(in), "hook")
	if code != 0 {
		s.t.Fatalf("hook exit %d", code)
	}
	if b, err := os.ReadFile(filepath.Join(s.home, ".claude", "ccanchor", "error.log")); err == nil {
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
		proj:          map[string]any{"allowedTools": []any{"Bash(ls)"}, "hasTrustDialogAccepted": true},
		proj + "/sub": map[string]any{"allowedTools": []any{}},
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
	must(t, os.WriteFile(filepath.Join(s.pdir(newp), "current.jsonl"), []byte(`{"type":"user","cwd":"`+newp+`"}`+"\n"), 0o600))
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
	if exists(s.pdir(proj)) || exists(s.pdir(proj+"/sub")) {
		t.Error("old project dirs still exist")
	}
	if got := listDir(t, s.pdir(newp)); !reflect.DeepEqual(got, []string{"current.jsonl", "memory", "s1.jsonl", "s2.jsonl"}) {
		t.Errorf("new project dir: %v", got)
	}
	moved := filepath.Join(s.pdir(newp), "s1.jsonl")
	if got := cwds(t, moved); !reflect.DeepEqual(got, []any{newp, newp + "/subdir", nil}) {
		t.Errorf("cwds: %v", got)
	}
	body, _ := os.ReadFile(moved)
	if !strings.Contains(string(body), "look at "+proj+"/x.py") {
		t.Error("message text should stay as it was")
	}
	if !strings.Contains(string(body), `{\"cwd\":\"`+proj+`\"}`) {
		t.Error("JSON inside strings should stay as it was")
	}
	if fi, _ := os.Stat(moved); fi.ModTime().Unix() != oldMtime {
		t.Error("mtime not kept")
	}
	if !isFile(filepath.Join(s.pdir(newp), "memory", "goal.md")) || !isFile(filepath.Join(s.pdir(newp+"/sub"), "s3.jsonl")) {
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
	if _, ok := pj[newp+"/sub"]; !ok {
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
		!isFile(filepath.Join(s.pdir(newp+"/sub"), "s3.jsonl")) {
		t.Error("mv did not carry the data")
	}
	if _, ok := s.gjProjects()[newp]; !ok {
		t.Error("settings not moved")
	}
}

func TestLinkMergesMemory(t *testing.T) {
	s := newSandbox(t)
	old := filepath.Join(s.work, "vscode-prj-managment") // gone before ccanchor saw it
	s.session(old, "o1", map[string]string{
		"MEMORY.md": "# Index\n- [Topic](topic.md) — old hook\n- [Only old](only.md)\n",
		"topic.md":  "old body\n", "only.md": "x\n",
	})
	newp := s.folder("oss", "vscode-prj-management")
	s.session(newp, "n1", map[string]string{"MEMORY.md": "# Index\n- [Topic](topic.md) — new hook\n", "topic.md": "new body\n"})
	_, out, _ := s.run("", "status")
	if !strings.Contains(out, "orphan") || !strings.Contains(out, "ccanchor link "+old+" "+newp) {
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
	raw := "{\n  \"zeta\": 1,\n  \"projects\": {\n    \"" + old + "\": {\n      \"allowedTools\": [\n        \"a && b > c\"\n      ]\n    },\n    \"/other\": {}\n  },\n  \"alpha\": 1795250653119\n}"
	must(t, os.WriteFile(filepath.Join(s.home, ".claude.json"), []byte(raw), 0o600))
	if code, out, errs := s.run("", "link", old, newp); code != 0 {
		t.Fatalf("link: %s %s", out, errs)
	}
	got, _ := os.ReadFile(filepath.Join(s.home, ".claude.json"))
	want := "{\n  \"zeta\": 1,\n  \"projects\": {\n    \"/other\": {},\n    \"" + newp + "\": {\n      \"allowedTools\": [\n        \"a && b > c\"\n      ]\n    }\n  },\n  \"alpha\": 1795250653119\n}"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestScanResolveFindsMovedDir(t *testing.T) {
	s := newSandbox(t)
	t.Setenv("HOME", s.root) // scan may climb up to the sandbox root
	a := s.folder("a", "proj")
	id, err := statIdent(a)
	must(t, err)
	if id.Birth == 0 {
		t.Skip("filesystem has no birth time")
	}
	e := &Entry{Path: a, Dev: id.Dev, Ino: id.Ino, Birth: id.Birth}
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
	if len(g) != 2 || g[0].Hooks[0].Command != "echo hi && true" || !strings.HasSuffix(g[1].Hooks[0].Command, "ccanchor/bin/ccanchor hook") {
		t.Fatalf("settings after install:\n%s", b)
	}
	if !strings.Contains(string(b), "echo hi && true") {
		t.Error("other hook was re-escaped")
	}
	if fi, err := os.Stat(installedBin()); err != nil || fi.Mode()&0o111 == 0 {
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
