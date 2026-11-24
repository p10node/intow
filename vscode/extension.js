'use strict';
// ccanchor for VS Code: runs `ccanchor sync --apply` when a window opens.
//
// The Claude Code extension reads its session list straight from
// ~/.claude/projects/<encoded folder path> and does not watch that folder.
// ccanchor's own SessionStart hook runs when the Claude panel starts a
// session, which can be after the list was drawn: the history is back on disk,
// but the list stays empty until it is reopened. Running the sync at window
// startup puts the data in place before anything reads it.

const { execFile } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const INSTALL_URL = 'https://github.com/p10node/ccanchor#install';

/** Environment Claude Code is launched with: the process env plus claudeCode.environmentVariables. */
function claudeEnv(base, vars) {
  const env = { ...base };
  if (Array.isArray(vars)) {
    for (const v of vars) {
      if (v && typeof v.name === 'string' && v.name && typeof v.value === 'string') env[v.name] = v.value;
    }
  }
  return env;
}

function expandHome(p, home) {
  if (p === '~') return home;
  return p.startsWith('~/') || p.startsWith('~\\') ? path.join(home, p.slice(2)) : p;
}

/** Places to look for the binary, most specific first. */
function candidates({ setting, env, home, platform, readdir }) {
  const exe = platform === 'win32' ? 'ccanchor.exe' : 'ccanchor';
  const list = [];
  if (setting) list.push(expandHome(setting, home));
  for (const dir of (env.PATH || env.Path || '').split(platform === 'win32' ? ';' : ':')) {
    if (dir) list.push(path.join(dir, exe));
  }
  if (platform !== 'win32') list.push('/opt/homebrew/bin/ccanchor', '/usr/local/bin/ccanchor');
  list.push(path.join(home, '.local', 'bin', exe));
  // The config dir in use, then the default one: with several profiles the
  // binary is usually installed once, in ~/.claude.
  for (const cfg of new Set([env.CLAUDE_CONFIG_DIR || path.join(home, '.claude'), path.join(home, '.claude')])) {
    list.push(path.join(cfg, 'ccanchor', 'bin', exe)); // `ccanchor install --copy`
    const data = path.join(cfg, 'plugins', 'data'); // the Claude Code plugin's own copy
    let names = [];
    try {
      names = readdir(data);
    } catch {
      // no plugins installed
    }
    for (const name of names.sort()) {
      if (name.includes('ccanchor')) list.push(path.join(data, name, 'bin', exe));
    }
  }
  return [...new Set(list)];
}

function findBinary(list, isExecutable) {
  return list.find((p) => isExecutable(p));
}

function isExecutable(p) {
  try {
    if (!fs.statSync(p).isFile()) return false;
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

/** The "moved OLD -> NEW (...)" lines of `ccanchor sync --apply`. */
function movedLines(stdout) {
  return String(stdout)
    .split(/\r?\n/)
    .filter((l) => l.startsWith('moved '))
    .map((l) => l.slice('moved '.length).trim());
}

function run(bin, args, env) {
  return new Promise((resolve) => {
    execFile(bin, args, { env, timeout: 20000, windowsHide: true }, (err, stdout, stderr) => {
      resolve({ code: err ? (typeof err.code === 'number' ? err.code : 1) : 0, stdout: String(stdout), stderr: String(stderr || (err && err.message) || '') });
    });
  });
}

function activate(context) {
  const vscode = require('vscode');
  const out = vscode.window.createOutputChannel('ccanchor');
  context.subscriptions.push(out);
  const log = (s) => out.appendLine(`${new Date().toISOString()} ${s}`);

  const locate = () => {
    const env = claudeEnv(process.env, vscode.workspace.getConfiguration('claudeCode').get('environmentVariables'));
    const list = candidates({
      setting: vscode.workspace.getConfiguration('ccanchor').get('path'),
      env,
      home: os.homedir(),
      platform: process.platform,
      readdir: (d) => fs.readdirSync(d),
    });
    return { env, bin: findBinary(list, isExecutable), list };
  };

  let running = Promise.resolve();
  const sync = (reason, interactive) => {
    running = running.then(async () => {
      const { env, bin, list } = locate();
      if (!bin) {
        log(`${reason}: ccanchor binary not found; looked in ${list.length} places`);
        const told = context.globalState.get('toldMissing');
        if (interactive || !told) {
          context.globalState.update('toldMissing', true);
          const pick = await vscode.window.showWarningMessage(
            'ccanchor: the ccanchor binary was not found. Install it, or set "ccanchor.path".',
            'How to install',
          );
          if (pick) vscode.env.openExternal(vscode.Uri.parse(INSTALL_URL));
        }
        return;
      }
      const res = await run(bin, ['sync', '--apply'], env);
      const moved = movedLines(res.stdout);
      log(`${reason}: ${bin} sync --apply -> exit ${res.code}, ${moved.length} moved`);
      for (const l of (res.stdout + res.stderr).split(/\r?\n/)) if (l.trim()) out.appendLine('    ' + l);
      if (moved.length > 0) {
        vscode.window.showInformationMessage(
          `ccanchor: Claude Code history followed ${moved.length === 1 ? moved[0] : moved.length + ' folders'}. ` +
            'If a session list was already open, refresh it.',
        );
      } else if (interactive) {
        vscode.window.showInformationMessage(res.code === 0 ? 'ccanchor: nothing moved.' : `ccanchor: ${res.stderr.trim() || 'failed'}`);
      }
    });
    return running;
  };

  context.subscriptions.push(
    vscode.commands.registerCommand('ccanchor.sync', () => sync('command', true)),
    vscode.commands.registerCommand('ccanchor.status', async () => {
      const { env, bin } = locate();
      out.show(true);
      if (!bin) return sync('status', true);
      const res = await run(bin, ['status'], env);
      out.appendLine(res.stdout + res.stderr);
    }),
    vscode.workspace.onDidChangeWorkspaceFolders(() => sync('workspace folders changed', false)),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('claudeCode.environmentVariables') || e.affectsConfiguration('ccanchor.path')) {
        sync('configuration changed', false);
      }
    }),
  );

  if (vscode.workspace.getConfiguration('ccanchor').get('syncOnStartup') !== false) {
    return sync('startup', false); // returned so tests can await it; VS Code does not wait on it for other extensions
  }
}

function deactivate() {}

module.exports = { activate, deactivate, claudeEnv, candidates, findBinary, movedLines, expandHome };
