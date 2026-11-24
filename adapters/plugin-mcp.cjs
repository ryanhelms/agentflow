// Hosts provide their project cwd. Keep stdin/stdout attached to the MCP server.
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync, writeFileSync } = require('node:fs');
const { tmpdir } = require('node:os');
const path = require('node:path');

const client = process.argv[2];
if (!['codex', 'cursor', 'gemini', 'vscode'].includes(client)) {
  process.stderr.write('devflow: expected a plugin client name\n');
  process.exit(1);
}

const windows = process.platform === 'win32';
const filename = windows ? 'devflow.ps1' : 'devflow';
let launcher = path.join(__dirname, '../templates/repo/.devflow/bin', filename);
if (typeof embeddedLaunchers !== 'undefined') {
  try {
    const directory = mkdtempSync(path.join(tmpdir(), 'devflow-launcher-'));
    process.once('exit', () => rmSync(directory, { recursive: true, force: true }));
    launcher = path.join(directory, filename);
    writeFileSync(launcher, windows ? embeddedLaunchers.windows : embeddedLaunchers.posix, { flag: 'wx', mode: 0o600 });
  } catch (error) {
    process.stderr.write('devflow: could not prepare the plugin launcher: ' + error.message + '\n');
    process.exit(1);
  }
}

const mcp = ['mcp', '--client', client];
const options = { shell: false, stdio: 'inherit' };
const server = windows
  ? spawn('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', launcher, ...mcp], options)
  : spawn('sh', [launcher, ...mcp], options);
server.on('error', error => {
  process.stderr.write('devflow: could not start the plugin launcher: ' + error.message + '\n');
  process.exitCode = 1;
});
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => server.kill(signal));
}
server.on('exit', (code, signal) => {
  process.exitCode = code ?? (signal === 'SIGINT' ? 130 : 1);
});
