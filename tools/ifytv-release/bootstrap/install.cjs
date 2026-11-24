#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

function install() {
  const windows = process.platform === 'win32';
  if (!windows && process.platform !== 'linux' && process.platform !== 'darwin') {
    throw new Error('unsupported platform ' + process.platform);
  }
  const filename = windows ? 'install.ps1' : 'install.sh';
  // The package may live inside a Yarn PnP ZIP which only JavaScript can read.
  const contents = fs.readFileSync(path.join(__dirname, filename));
  const stage = fs.mkdtempSync(path.join(os.tmpdir(), 'ifytv-launcher-'));
  const script = path.join(stage, filename);
  try {
    fs.writeFileSync(script, contents, { flag: 'wx', mode: 0o600 });
    const args = process.argv.slice(2);
    const options = { stdio: 'inherit' };
    let child;
    if (windows) {
      const powershell = process.env.SystemRoot
        ? path.join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe')
        : 'powershell.exe';
      const parameters = ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script, ...args];
      child = spawnSync(powershell, parameters, options);
      if (child.error && child.error.code === 'ENOENT') {
        child = spawnSync('pwsh.exe', parameters, options);
      }
    } else {
      child = spawnSync('sh', [script, ...args], options);
    }
    if (child.error) throw child.error;
    if (child.signal) return 128 + os.constants.signals[child.signal];
    return child.status;
  } finally {
    fs.rmSync(script, { force: true });
    fs.rmdirSync(stage);
  }
}

try {
  process.exitCode = install();
} catch (error) {
  console.error('ifytv: ' + error.message);
  process.exitCode = 1;
}
