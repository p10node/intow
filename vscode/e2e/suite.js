'use strict';
// Runs inside VS Code's extension host once the window has started.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vscode = require('vscode');

const enc = (p) => p.replace(/[^a-zA-Z0-9]/g, '-');

exports.run = async () => {
  const { E2E_CFG: cfg, E2E_OLD: a, E2E_NEW: b } = process.env;
  const moved = path.join(cfg, 'projects', enc(b), 's1.jsonl');
  const ext = vscode.extensions.getExtension('p10node.ccanchor');
  assert.ok(ext, 'extension not installed');
  const doneBeforeTests = fs.existsSync(moved);
  for (let i = 0; i < 150 && !fs.existsSync(moved); i++) await new Promise((r) => setTimeout(r, 100));

  assert.ok(ext.isActive, 'the extension was not activated at startup');
  const seen = vscode.workspace.getConfiguration('claudeCode').get('environmentVariables');
  assert.ok(fs.existsSync(moved), `the history did not follow the renamed folder (claudeCode.environmentVariables as read: ${JSON.stringify(seen)})`);
  assert.ok(!fs.existsSync(path.join(cfg, 'projects', enc(a))), 'the old project folder is still there');
  assert.strictEqual(JSON.parse(fs.readFileSync(moved, 'utf8').split('\n')[0]).cwd, b);
  const gj = JSON.parse(fs.readFileSync(path.join(cfg, '.claude.json'), 'utf8'));
  assert.deepStrictEqual(Object.keys(gj.projects), [b]);

  const cmds = await vscode.commands.getCommands(true);
  assert.ok(cmds.includes('ccanchor.sync') && cmds.includes('ccanchor.status'), 'commands not registered');
  console.log(`E2E OK (${vscode.env.appName} ${vscode.version}): history followed at startup; done before the test started: ${doneBeforeTests}`);
};
