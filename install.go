package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const hookMatcher = "startup|resume"

// hookBinary is the path the hook runs. A ccanchor found on PATH (e.g. Homebrew's
// /opt/homebrew/bin/ccanchor) is used as is so upgrades apply; otherwise the
// running binary is copied to ~/.claude/ccanchor/bin.
func hookBinary(forceCopy bool) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	selfReal, err := filepath.EvalSymlinks(self)
	if err != nil {
		selfReal = self
	}
	if !forceCopy {
		if p, err := exec.LookPath("ccanchor"); err == nil {
			if r, err := filepath.EvalSymlinks(p); err == nil && r == selfReal {
				return filepath.Abs(p)
			}
		}
	}
	dst := installedBin()
	if selfReal != dst {
		if err := copyFile(selfReal, dst, 0o755); err != nil {
			return "", err
		}
	}
	return dst, nil
}

func isOurHook(cmd string) bool {
	return strings.Contains(cmd, "ccanchor") && strings.HasSuffix(strings.TrimSpace(cmd), " hook")
}

func loadSettings() (*omap, []byte, error) {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return newOmap(), nil, nil
		}
		return nil, nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return newOmap(), raw, nil
	}
	m, err := parseObject(raw)
	if err != nil {
		return nil, raw, errors.New(settingsPath() + " is not a JSON object; fix it first")
	}
	return m, raw, nil
}

func saveSettings(m *omap) (string, error) {
	b := backup(settingsPath())
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0o755); err != nil {
		return "", err
	}
	return b, atomicWrite(settingsPath(), append(pretty(m.marshal()), '\n'), 0o644)
}

// stripOurHooks removes ccanchor's SessionStart hook, leaving every other hook
// byte-for-byte as it was.
func stripOurHooks(root *omap) bool {
	hraw, ok := root.get("hooks")
	if !ok {
		return false
	}
	hooks, err := parseObject(hraw)
	if err != nil {
		return false
	}
	sraw, ok := hooks.get("SessionStart")
	if !ok {
		return false
	}
	var groups []json.RawMessage
	if json.Unmarshal(sraw, &groups) != nil {
		return false
	}
	changed := false
	var kept []json.RawMessage
	for _, g := range groups {
		gm, err := parseObject(g)
		if err != nil {
			kept = append(kept, g)
			continue
		}
		var hs []json.RawMessage
		if hr, ok := gm.get("hooks"); !ok || json.Unmarshal(hr, &hs) != nil {
			kept = append(kept, g)
			continue
		}
		var keep []json.RawMessage
		for _, h := range hs {
			var hc struct {
				Command string `json:"command"`
			}
			json.Unmarshal(h, &hc)
			if isOurHook(hc.Command) {
				changed = true
				continue
			}
			keep = append(keep, h)
		}
		switch {
		case len(keep) == len(hs):
			kept = append(kept, g)
		case len(keep) > 0:
			b, _ := encodeJSON(keep, false)
			gm.set("hooks", b)
			kept = append(kept, gm.marshal())
		}
	}
	if !changed {
		return false
	}
	if len(kept) == 0 {
		hooks.del("SessionStart")
	} else {
		b, _ := encodeJSON(kept, false)
		hooks.set("SessionStart", b)
	}
	if len(hooks.keys) == 0 {
		root.del("hooks")
	} else {
		root.set("hooks", hooks.marshal())
	}
	return true
}

func addOurHook(root *omap, command string) {
	hooks := newOmap()
	if hraw, ok := root.get("hooks"); ok {
		if h, err := parseObject(hraw); err == nil {
			hooks = h
		}
	}
	var groups []json.RawMessage
	if sraw, ok := hooks.get("SessionStart"); ok {
		json.Unmarshal(sraw, &groups)
	}
	hook := newOmap()
	hook.set("type", json.RawMessage(`"command"`))
	cmdJSON, _ := encodeJSON(command, false)
	hook.set("command", cmdJSON)
	hook.set("timeout", json.RawMessage(`30`))
	group := newOmap()
	matcher, _ := encodeJSON(hookMatcher, false)
	group.set("matcher", matcher)
	group.set("hooks", json.RawMessage("["+string(hook.marshal())+"]"))
	groups = append(groups, group.marshal())
	b, _ := encodeJSON(groups, false)
	hooks.set("SessionStart", b)
	root.set("hooks", hooks.marshal())
}

func (c *cli) cmdInstall(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"copy"}, []string{"bin-dir"})
	if err != nil {
		return 2, err
	}
	bin, err := hookBinary(o.has("copy"))
	if err != nil {
		return 1, err
	}
	settings, _, err := loadSettings()
	if err != nil {
		return 1, err
	}
	stripOurHooks(settings)
	addOurHook(settings, hookCommand(bin))
	b, err := saveSettings(settings)
	if err != nil {
		return 1, err
	}
	c.printf("hook: %s hook\n", bin)
	if b != "" {
		c.printf("added SessionStart hook to %s (backup: %s)\n", settingsPath(), b)
	} else {
		c.printf("added SessionStart hook to %s\n", settingsPath())
	}
	if dir := o.vals["bin-dir"]; dir != "" {
		link := filepath.Join(absClean(dir), "ccanchor"+exeSuffix)
		os.MkdirAll(filepath.Dir(link), 0o755)
		os.Remove(link)
		if err := os.Symlink(bin, link); err != nil {
			return 1, err
		}
		c.printf("linked %s\n", link)
	}
	return c.cmdScan(nil)
}

func (c *cli) cmdUninstall(argv []string) (int, error) {
	o, err := parseArgs(argv, []string{"purge"}, nil)
	if err != nil {
		return 2, err
	}
	settings, _, err := loadSettings()
	if err != nil {
		return 1, err
	}
	if stripOurHooks(settings) {
		if _, err := saveSettings(settings); err != nil {
			return 1, err
		}
		c.printf("removed SessionStart hook from %s\n", settingsPath())
	} else {
		c.printf("no ccanchor hook in %s\n", settingsPath())
	}
	if o.has("purge") {
		if err := os.RemoveAll(stateDir()); err != nil {
			return 1, err
		}
		c.printf("removed %s\n", stateDir())
	} else {
		c.printf("kept %s (registry, log, backups); --purge removes it\n", stateDir())
	}
	return 0, nil
}
