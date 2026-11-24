'use strict';
// Starts a real VS Code with this extension and a project folder that was
// renamed after intow last saw it, then runs suite.js inside it.
//   INTOW_BIN=/path/to/intow node e2e/run.js      (see e2e/docker.sh)
const cp = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { runTests } = require('@vscode/test-electron');

const enc = (p) => p.replace(/[^a-zA-Z0-9]/g, '-');

(async () => {
  const bin = process.env.INTOW_BIN;
  if (!bin) throw new Error('set INTOW_BIN');
  // Not under the temp dir: intow never tracks folders there.
  const root = fs.mkdtempSync(path.join(os.homedir(), '.intow-vscode-e2e-'));
  const cfg = path.join(root, 'claude');
  const a = path.join(root, 'work', 'projA');
  const b = path.join(root, 'work', 'projB');
  fs.mkdirSync(a, { recursive: true });
  fs.mkdirSync(path.join(cfg, 'projects', enc(a)), { recursive: true });
  fs.writeFileSync(path.join(cfg, 'projects', enc(a), 's1.jsonl'), JSON.stringify({ type: 'user', cwd: a, sessionId: 's1' }) + '\n');
  fs.writeFileSync(path.join(cfg, '.claude.json'), JSON.stringify({ projects: { [a]: { hasTrustDialogAccepted: true } } }, null, 2));
  cp.execFileSync(bin, ['scan', '-q'], { env: { ...process.env, CLAUDE_CONFIG_DIR: cfg } });
  fs.renameSync(a, b);

  // The config dir reaches the extension the way it reaches Claude Code: through its setting.
  const userData = path.join(root, 'user-data');
  fs.mkdirSync(path.join(userData, 'User'), { recursive: true });
  fs.writeFileSync(
    path.join(userData, 'User', 'settings.json'),
    JSON.stringify({ 'intow.path': bin, 'claudeCode.environmentVariables': [{ name: 'CLAUDE_CONFIG_DIR', value: cfg }] }),
  );
  const env = { ...process.env };
  delete env.CLAUDE_CONFIG_DIR;
  try {
    await runTests({
      extensionDevelopmentPath: path.resolve(__dirname, '..'),
      extensionTestsPath: path.resolve(__dirname, 'suite.js'),
      launchArgs: [b, '--user-data-dir', userData, '--disable-extensions', '--no-sandbox'],
      extensionTestsEnv: { ...env, E2E_CFG: cfg, E2E_OLD: a, E2E_NEW: b },
    });
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
