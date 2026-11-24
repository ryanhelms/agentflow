// Codex's compatibility manifest starts servers in the user's project, but does
// not expand plugin-root paths in arguments. Embed the same signed-release
// launchers shipped to the other hosts, keeping the project's working directory.
import { readFile, writeFile } from 'node:fs/promises';

const root = new URL('../', import.meta.url);
const read = path => readFile(new URL(path, root), 'utf8');
const [start, posix, windows] = await Promise.all([
  read('adapters/plugin-mcp.cjs'),
  read('templates/repo/.agentflow/bin/agentflow'),
  read('templates/repo/.agentflow/bin/agentflow.ps1'),
]);
const args = ['-e', start, '--', '--embedded-launchers', 'codex', posix, windows];
const output = JSON.stringify({ mcpServers: { agentflow: { command: 'node', args, startup_timeout_sec: 60 } } }, null, 2) + '\n';
const path = new URL('adapters/codex/plugin-mcp.json', root);
if (process.argv.includes('--check')) {
  if (await readFile(path, 'utf8') !== output) {
    throw new Error('Regenerate the Codex launcher with node scripts/build-codex-plugin.mjs');
  }
} else {
  await writeFile(path, output);
}
