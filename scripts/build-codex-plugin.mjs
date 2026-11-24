// Codex's compatibility manifest starts servers in the user's project, but does
// not expand plugin-root paths in arguments. Embed the same signed-release
// launchers shipped to the other hosts, keeping the project's working directory.
import { readFile, writeFile } from 'node:fs/promises';

const root = new URL('../', import.meta.url);
const read = async path => (await readFile(path, 'utf8')).replaceAll('\r\n', '\n');
const [start, posix, windows] = await Promise.all([
  read(new URL('adapters/plugin-mcp.cjs', root)),
  read(new URL('templates/repo/.devflow/bin/devflow', root)),
  read(new URL('templates/repo/.devflow/bin/devflow.ps1', root)),
]);
const launchers = `const embeddedLaunchers = ${JSON.stringify({ posix, windows })};\n`;
const args = ['-e', launchers + start, '--', '--embedded-launchers', 'codex'];
const output = JSON.stringify({ mcpServers: { devflow: { command: 'node', args, startup_timeout_sec: 60 } } }, null, 2) + '\n';
const path = new URL('adapters/codex/plugin-mcp.json', root);
if (process.argv.includes('--check')) {
  if (await read(path) !== output) {
    throw new Error('Regenerate the Codex launcher with node scripts/build-codex-plugin.mjs');
  }
} else {
  await writeFile(path, output);
}
