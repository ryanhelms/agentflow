// Hosts provide their project cwd. Keep stdin/stdout attached to the MCP server.
const { spawn } = require('node:child_process');
const path = require('node:path');

const [client, posixSource, windowsSource] = process.argv.slice(2);
if (!['codex', 'cursor', 'gemini', 'vscode'].includes(client)) {
  process.stderr.write('agentflow: expected a plugin client name\n');
  process.exit(1);
}
const embedded = process.argv[1] === '--embedded-launchers';
const launcher = path.resolve(__dirname, '../templates/repo/.agentflow/bin/agentflow');
const mcp = ['mcp', '--client', client];

let command = 'sh';
let args = embedded ? ['-c', posixSource, 'agentflow', ...mcp] : [launcher, ...mcp];
if (process.platform === 'win32') {
  command = 'powershell.exe';
  const script = embedded
    ? ['-Command', '& { ' + windowsSource + '\n} mcp --client ' + client]
    : ['-File', launcher + '.ps1', ...mcp];
  args = ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', ...script];
}

const server = spawn(command, args, { stdio: 'inherit' });
server.on('error', error => {
  process.stderr.write('agentflow: could not start the plugin launcher: ' + error.message + '\n');
  process.exitCode = 1;
});
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => server.kill(signal));
}
server.on('exit', (code, signal) => {
  process.exitCode = code ?? (signal === 'SIGINT' ? 130 : 1);
});
