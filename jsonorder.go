package main

import (
	"bytes"
	"encoding/json"
	"errors"
)

// omap is a JSON object that keeps its key order and leaves values as raw
// JSON, so files owned by Claude Code (~/.claude.json, settings.json) are
// rewritten with only the intended change.
type omap struct {
	keys []string
	vals map[string]json.RawMessage
}

var errNotObject = errors.New("not a JSON object")

func newOmap() *omap { return &omap{vals: map[string]json.RawMessage{}} }

func parseObject(b []byte) (*omap, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errNotObject
	}
	m := newOmap()
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := t.(string)
		if !ok {
			return nil, errNotObject
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		m.set(key, raw)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *omap) get(k string) (json.RawMessage, bool) { v, ok := m.vals[k]; return v, ok }

func (m *omap) set(k string, v json.RawMessage) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func (m *omap) del(k string) {
	if _, ok := m.vals[k]; !ok {
		return
	}
	delete(m.vals, k)
	for i, key := range m.keys {
		if key == k {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

func (m *omap) marshal() json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := encodeJSON(k, false)
		buf.Write(kb)
		buf.WriteByte(':')
		var c bytes.Buffer
		if json.Compact(&c, m.vals[k]) == nil {
			buf.Write(c.Bytes())
		} else {
			buf.Write(m.vals[k])
		}
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// pretty formats JSON the way JSON.stringify(v, null, 2) does.
func pretty(raw []byte) []byte {
	var c, out bytes.Buffer
	if json.Compact(&c, raw) != nil || json.Indent(&out, c.Bytes(), "", "  ") != nil {
		return raw
	}
	return out.Bytes()
}

func jsonKind(raw json.RawMessage) byte {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[', '"':
			return c
		case 't', 'f':
			return 'b'
		default:
			return c
		}
	}
	return 0
}

// mergeSettings merges two ~/.claude.json project entries. b (the entry that
// already exists for the new path) wins on scalars; lists are combined,
// objects are merged shallowly, and booleans are OR-ed (trust stays granted).
func mergeSettings(a, b json.RawMessage) json.RawMessage {
	ao, err1 := parseObject(a)
	bo, err2 := parseObject(b)
	if err1 != nil || err2 != nil {
		return b
	}
	for _, k := range bo.keys {
		bv := bo.vals[k]
		av, ok := ao.get(k)
		if !ok {
			ao.set(k, bv)
			continue
		}
		switch ka, kb := jsonKind(av), jsonKind(bv); {
		case ka == '[' && kb == '[':
			ao.set(k, unionArrays(av, bv))
		case ka == '{' && kb == '{':
			x, e1 := parseObject(av)
			y, e2 := parseObject(bv)
			if e1 != nil || e2 != nil {
				ao.set(k, bv)
				break
			}
			for _, yk := range y.keys {
				x.set(yk, y.vals[yk])
			}
			ao.set(k, x.marshal())
		case ka == 'b' && kb == 'b':
			var x, y bool
			json.Unmarshal(av, &x)
			json.Unmarshal(bv, &y)
			nb, _ := json.Marshal(x || y)
			ao.set(k, nb)
		default:
			ao.set(k, bv)
		}
	}
	return ao.marshal()
}

func unionArrays(a, b json.RawMessage) json.RawMessage {
	var xa, xb []json.RawMessage
	if json.Unmarshal(a, &xa) != nil || json.Unmarshal(b, &xb) != nil {
		return b
	}
	seen := map[string]bool{}
	norm := func(r json.RawMessage) string {
		var c bytes.Buffer
		json.Compact(&c, r)
		return c.String()
	}
	for _, v := range xa {
		seen[norm(v)] = true
	}
	for _, v := range xb {
		if !seen[norm(v)] {
			xa = append(xa, v)
			seen[norm(v)] = true
		}
	}
	out, err := encodeJSON(xa, false)
	if err != nil {
		return b
	}
	return out
}
