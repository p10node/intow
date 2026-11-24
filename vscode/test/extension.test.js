'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const Module = require('module');

const ext = require('../extension.js');

test('claudeEnv layers claudeCode.environmentVariables over the process env', () => {
  const env = ext.claudeEnv({ PATH: '/bin', A: '1' }, [{ name: 'CLAUDE_CONFIG_DIR', value: '/cfg' }, { name: 'A', value: '2' }, { name: '', value: 'x' }, null, { name: 'B' }]);
  assert.deepStrictEqual(env, { PATH: '/bin', A: '2', CLAUDE_CONFIG_DIR: '/cfg' });
  assert.deepStrictEqual(ext.claudeEnv({ A: '1' }, undefined), { A: '1' });
});

test('candidates: setting, PATH, usual folders, config dir, plugin data', () => {
  const list = ext.candidates({
    setting: '~/tools/intow',
    env: { PATH: '/a:/b', CLAUDE_CONFIG_DIR: '/cfg' },
    home: '/home/u',
    platform: 'linux',
    readdir: (d) => {
      if (d === '/cfg/plugins/data') return ['other-plugin', 'intow-intow'];
      assert.strictEqual(d, '/home/u/.claude/plugins/data');
      throw new Error('ENOENT');
    },
  });
  assert.deepStrictEqual(list, [
    '/home/u/tools/intow',
    '/a/intow',
    '/b/intow',
    '/opt/homebrew/bin/intow',
    '/usr/local/bin/intow',
    '/home/u/.local/bin/intow',
    '/cfg/intow/bin/intow',
    '/cfg/plugins/data/intow-intow/bin/intow',
    '/home/u/.claude/intow/bin/intow', // installed once, used from every profile
  ]);
});

test('candidates: default config dir, no plugins folder', () => {
  const list = ext.candidates({
    setting: '',
    env: {},
    home: '/home/u',
    platform: 'linux',
    readdir: () => {
      throw new Error('ENOENT');
    },
  });
  assert.ok(list.includes('/home/u/.claude/intow/bin/intow'));
  assert.ok(!list.some((p) => p.includes('plugins')));
});

test('movedLines picks the migrations out of the sync output', () => {
  const out = 'moved /a -> /b (2 sessions, settings)\n    projects/-a -> -b  [rename]\nskip /c -> /d: Claude is still running there\n';
  assert.deepStrictEqual(ext.movedLines(out), ['/a -> /b (2 sessions, settings)']);
  assert.deepStrictEqual(ext.movedLines('nothing moved\n'), []);
});

// ---- activate(), against a stand-in for the vscode module and a stand-in binary

function fakeVscode(config) {
  const calls = { info: [], warn: [], commands: {}, lines: [] };
  const vscode = {
    window: {
      createOutputChannel: () => ({ appendLine: (l) => calls.lines.push(l), show: () => {}, dispose: () => {} }),
      showInformationMessage: async (m) => void calls.info.push(m),
      showWarningMessage: async (m) => void calls.warn.push(m),
    },
    workspace: {
      getConfiguration: (section) => ({ get: (key) => config[`${section}.${key}`] }),
      onDidChangeWorkspaceFolders: () => ({ dispose: () => {} }),
      onDidChangeConfiguration: () => ({ dispose: () => {} }),
    },
    commands: { registerCommand: (id, fn) => ((calls.commands[id] = fn), { dispose: () => {} }) },
    env: { openExternal: () => {} },
    Uri: { parse: (s) => s },
  };
  return { vscode, calls };
}

function withVscode(vscode, fn) {
  const load = Module._load;
  Module._load = function (request, ...rest) {
    return request === 'vscode' ? vscode : load.call(this, request, ...rest);
  };
  return Promise.resolve()
    .then(fn)
    .finally(() => {
      Module._load = load;
    });
}

function context() {
  const state = new Map();
  return { subscriptions: [], globalState: { get: (k) => state.get(k), update: (k, v) => void state.set(k, v) } };
}

test('activate runs `sync --apply` with the config dir Claude Code uses, and reports a move', { skip: process.platform === 'win32' }, async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'intow-vscode-'));
  const bin = path.join(dir, 'intow');
  const record = path.join(dir, 'ran');
  fs.writeFileSync(bin, `#!/bin/sh\necho "$CLAUDE_CONFIG_DIR $*" >> "${record}"\necho "moved /old -> /new (1 session)"\n`, { mode: 0o755 });
  const { vscode, calls } = fakeVscode({
    'intow.path': bin,
    'claudeCode.environmentVariables': [{ name: 'CLAUDE_CONFIG_DIR', value: '/profile/x' }],
  });
  await withVscode(vscode, () => ext.activate(context()));
  assert.strictEqual(fs.readFileSync(record, 'utf8'), '/profile/x sync --apply\n');
  assert.strictEqual(calls.info.length, 1);
  assert.match(calls.info[0], /followed \/old -> \/new/);
  assert.deepStrictEqual(Object.keys(calls.commands).sort(), ['intow.status', 'intow.sync']);
  fs.rmSync(dir, { recursive: true, force: true });
});

test('activate stays quiet when nothing moved, and does nothing when turned off', { skip: process.platform === 'win32' }, async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'intow-vscode-'));
  const bin = path.join(dir, 'intow');
  const record = path.join(dir, 'ran');
  fs.writeFileSync(bin, `#!/bin/sh\necho x >> "${record}"\necho "nothing moved"\n`, { mode: 0o755 });
  let f = fakeVscode({ 'intow.path': bin });
  await withVscode(f.vscode, () => ext.activate(context()));
  assert.deepStrictEqual([f.calls.info, f.calls.warn], [[], []]);
  assert.strictEqual(fs.readFileSync(record, 'utf8'), 'x\n');
  f = fakeVscode({ 'intow.path': bin, 'intow.syncOnStartup': false });
  await withVscode(f.vscode, () => ext.activate(context()));
  assert.strictEqual(fs.readFileSync(record, 'utf8'), 'x\n');
  fs.rmSync(dir, { recursive: true, force: true });
});

test('a missing binary is reported once, not at every window', async () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'intow-vscode-home-'));
  const saved = { HOME: process.env.HOME, USERPROFILE: process.env.USERPROFILE, PATH: process.env.PATH };
  Object.assign(process.env, { HOME: home, USERPROFILE: home, PATH: home });
  try {
    const { vscode, calls } = fakeVscode({ 'intow.path': path.join(home, 'nope') });
    const ctx = context();
    const found = ext.findBinary(ext.candidates({ setting: '', env: process.env, home, platform: process.platform, readdir: () => [] }), (p) => fs.existsSync(p));
    if (found) return; // a system-wide install (/usr/local/bin): nothing to check here
    await withVscode(vscode, () => ext.activate(ctx));
    await withVscode(vscode, () => ext.activate(ctx));
    assert.strictEqual(calls.warn.length, 1);
  } finally {
    Object.assign(process.env, saved);
    fs.rmSync(home, { recursive: true, force: true });
  }
});
