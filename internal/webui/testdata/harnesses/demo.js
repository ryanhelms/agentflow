"use strict";

// Scripted harness states for looking at the Harnesses page
// (TestHarnessesDemo in harnesses_browser_test.go). It runs in the page and
// writes the stream frames lane 6.2's harnesses projection will send. It is
// for design review only; no harness is started.
//
//   /w/first/              eight harnesses, one in each state
//   /w/first/?view=empty   nothing found on PATH
//   /w/first/?view=off     the daemon runs no harness detection
//   /w/first/?view=manual  automatic probes are off and nothing was probed
//   /w/first/?view=probing a probe is running
//
// Probe now works: the fake endpoint accepts the request, and this script
// answers it with a "probing" frame and, two seconds later, the results.
(async () => {
  const byID = id => document.getElementById(id);
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const repo = "3f2a9c1b7d4e5f60";
  let seq = 0;
  const emit = text => fetch("/fixture/stream/emit", {method: "POST", body: text});
  const frame = (event, data) => "event: " + event + "\nid: v1:" + repo + "=" + (++seq) + "\ndata: " + JSON.stringify(data) + "\n\n";
  const at = ms => new Date(Date.now() + ms).toISOString();
  const overview = {projection: "overview", repo, worktree: "first", state: {branch: "issue-103-harnesses", head: "1111111", oid: "1111111aaaaaaa1111111aaaaaaa1111111aaaa", detached: false, changed: 0, staged: 0, unstaged: 0, untracked: 0, operation: "", config: {valid: true, problems: []}}};
  const view = new URLSearchParams(location.search).get("view") || "";

  const ok = ms => ({status: "ok", code: "ok", probed_at: at(-ms), duration_ms: 812});
  const acp = (extra = {}) => ({protocol_version: 1, load_session: true, image: true, audio: false, embedded_context: true, mcp_http: true, mcp_sse: false, logout: false, ...extra});
  const gemini = {id: "gemini", name: "Gemini CLI", description: "Google's open-source AI agent for the terminal.", kind: "native", state: "native", code: "ok", sources: ["registry", "agentflow"],
    command: "gemini", args: ["--acp"], path: "~/.local/bin/gemini", version: "0.71.1", registry_version: "0.71.1", probe: ok(600000),
    acp: acp({agent_name: "gemini-cli", agent_version: "0.71.1"}),
    sign_in: [{method: "oauth-personal", type: "agent", name: "Log in with Google"}, {method: "gemini-api-key", type: "agent", name: "Use a Gemini API key", description: "Requires the GEMINI_API_KEY environment variable."}],
    auth: {status: "unknown", code: "no_check"}};
  const claude = {id: "claude-acp", name: "Claude Agent", description: "ACP adapter for Claude Code.", kind: "adapter", state: "adapter_installed", code: "ok", sources: ["registry", "agentflow"],
    command: "claude-agent-acp", path: "~/Library/Application Support/fnm/node-versions/v24.11.1/installation/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/bin/claude-agent-acp",
    base: {name: "Claude Code", command: "claude", path: "~/.local/bin/claude"}, version: "0.31.0", registry_version: "0.33.1", probe: ok(540000),
    acp: acp({agent_name: "claude-agent-acp", agent_title: "Claude Agent", agent_version: "0.31.0", audio: false, mcp_sse: true}),
    sign_in: [{method: "claude-login", type: "terminal", name: "Log in with Claude", command: "claude-agent-acp --cli auth login"}],
    auth: {status: "signed_in", code: "ok", checked_at: at(-540000)}};
  const codex = {id: "codex-acp", name: "Codex CLI", description: "ACP adapter for OpenAI's coding assistant.", kind: "adapter", state: "needs_auth", code: "signed_out", sources: ["registry", "agentflow"],
    command: "codex-acp", path: "/opt/homebrew/bin/codex-acp", base: {name: "Codex", command: "codex", path: "/opt/homebrew/bin/codex"}, version: "0.12.0", registry_version: "0.12.0", probe: ok(480000),
    acp: acp({agent_name: "codex-acp", agent_version: "0.12.0", image: true, logout: true}),
    sign_in: [{method: "chatgpt", type: "terminal", name: "Sign in with ChatGPT", command: "codex-acp login"}, {method: "openai-api-key", type: "agent", name: "Use an OpenAI API key", description: "Requires the OPENAI_API_KEY environment variable."},
      {method: "device", type: "terminal", name: "Device code", unsafe: true}],
    auth: {status: "signed_out", code: "signed_out", checked_at: at(-480000)},
    fix: {text: "Sign in to Codex CLI, then probe again.", command: "codex login"}};
  const cursor = (probed = false) => ({id: "cursor", name: "Cursor", description: "Cursor's coding agent.", kind: "native", state: "native", code: probed ? "ok" : "not_probed", sources: ["registry"],
    command: "cursor-agent", args: ["acp"], path: "~/.local/bin/cursor-agent", version: "2026.11.20", registry_version: "2026.11.20",
    probe: probed ? {status: "ok", code: "ok", probed_at: at(0), duration_ms: 1304} : {status: "none", code: "not_probed"},
    ...(probed ? {acp: acp({agent_name: "cursor-agent"}), sign_in: [{method: "cursor-login", type: "agent", name: "Cursor account"}]} : {fix: {text: "Not checked yet. Probe it to confirm it speaks ACP.", command: "agentflow ai harnesses --probe"}}),
    auth: {status: "unknown", code: "no_check"}});
  const opencode = {id: "opencode", name: "OpenCode", kind: "native", state: "native", code: "timeout", sources: ["registry", "personal"],
    command: "opencode", args: ["acp"], path: "~/.opencode/bin/opencode", version: "", version_code: "no_version", registry_version: "1.4.2",
    probe: {status: "failed", code: "timeout", probed_at: at(-420000), duration_ms: 5000}, auth: {status: "unknown", code: "no_check"},
    fix: {text: "`opencode` did not answer within 5s. Probe again, or allow more time.", command: "agentflow config set ai.harnesses.probe_timeout=15s"}, notes: ["registry_env_ignored"]};
  const goose = {id: "goose", name: "goose", description: "A local, extensible, open source AI agent.", kind: "native", state: "incompatible", code: "protocol_version", sources: ["registry"],
    command: "goose", args: ["acp"], path: "/usr/local/bin/goose", version: "1.9.0", registry_version: "1.14.0",
    probe: {status: "failed", code: "protocol_version", probed_at: at(-400000), duration_ms: 240}, auth: {status: "unknown", code: "no_check"},
    fix: {text: "goose speaks ACP version 2; agentflow needs version 1. Update goose.", url: "https://github.com/block/goose"}};
  const pi = {id: "pi-acp", name: "pi ACP", description: "ACP adapter for the pi coding agent.", kind: "adapter", state: "adapter_available", code: "base_only", sources: ["registry"],
    command: "pi-acp", base: {name: "pi", command: "pi", path: "~/.local/share/mise/installs/node/24.11.1/bin/pi"}, registry_version: "0.0.26",
    probe: {status: "none", code: "not_probed"}, auth: {status: "unknown", code: "base_missing"},
    fix: {text: "pi is installed, but its ACP adapter is not. Install the adapter yourself; agentflow never installs it.", command: "npm install -g pi-acp@0.0.26", url: "https://github.com/svkozak/pi-acp"}};
  const mine = {id: "my-agent", name: "My agent", kind: "native", state: "native", code: "ok", sources: ["personal"],
    command: "/Users/sam/Projects/experiments/agents/my-agent/target/release/my-agent", args: ["acp", "--profile", "work"], path: "~/Projects/experiments/agents/my-agent/target/release/my-agent",
    version: "0.1.0-dev+20261120", probe: {status: "cached", code: "ok", probed_at: at(-20 * 3600000), duration_ms: 96},
    acp: acp({agent_name: "my-agent", agent_version: "0.1.0-dev", load_session: false, image: false, embedded_context: false, mcp_http: false}),
    sign_in: [{method: "token", type: "terminal", name: "Paste a token", command: "MY_AGENT_PROFILE=work my-agent acp login --token-stdin"}],
    auth: {status: "unknown", code: "env_credential", checked_at: at(-20 * 3600000)},
    fix: {text: "You may be signed in with an environment variable; agentflow cannot check that."}, quarantined: true, notes: ["quarantined"]};

  const base = {detector: true, registry: {commit: "4273056f03b93f8ce41e9276256ea47b057b794c", committed_at: "2026-11-26T23:54:34Z", agents: 46}, auto_probe: true, checked_at: at(-5000)};
  const all = probed => [gemini, claude, mine, codex, cursor(probed), opencode, goose, pi];
  const manual = harness => ["adapter_available"].includes(harness.state) ? harness : {...harness, state: harness.kind === "adapter" ? "adapter_installed" : "native", code: "not_probed", acp: undefined, sign_in: undefined, version: harness.version,
    probe: {status: "refused", code: "needs_person"}, auth: {status: "unknown", code: "needs_person"},
    fix: {text: "Automatic probes are off. Probe it yourself, or turn them on.", command: "agentflow ai harnesses --probe"}};
  function state(probed = false, probing = []) {
    if (view === "empty") return {...base, harnesses: []};
    if (view === "off") return {detector: false, registry: base.registry, auto_probe: false, harnesses: [], checked_at: ""};
    if (view === "manual" && !probed) return {...base, auto_probe: false, harnesses: all(false).map(manual), probing};
    return {...base, auto_probe: view !== "manual", harnesses: all(probed), probing: view === "probing" && !probed ? ["cursor", "opencode"] : probing, checked_at: at(probed ? 0 : -5000),
      skipped_path: [{dir: "~/GitHub/agentflow/bin", reason: "inside_repository"}, {dir: "~/GitHub/agentflow/node_modules/.bin", reason: "node_modules"}, {dir: "/usr/local/shared/tools", reason: "unsafe_permissions"}],
      problems: [{layer: "personal", id: "codex-acp", code: "launcher", message: "command \"npx\" is a package runner; name the installed program instead"}]};
  }
  const entry = value => ({projection: "harnesses", repo, state: value});

  // A stored session opens the console by itself; otherwise launch one.
  while (byID("connection-options").hidden && byID("console-shell").hidden) await wait(20);
  if (byID("console-shell").hidden) {
    const ticket = await (await fetch("/fixture/ticket", {method: "POST"})).text();
    location.hash = new URLSearchParams({launch: ticket, screen: "overview"}).toString();
    while (byID("console-shell").hidden) await wait(20);
  }
  location.hash = "#harnesses";
  await emit(frame("snapshot", {cursor: "v1:" + repo + "=1", states: [overview, entry(state())]}));
  document.documentElement.dataset.demo = "ready";

  // Answer each probe request the fake endpoint accepted.
  const probes = async () => (await (await fetch("/fixture/harnesses/requests")).json()).filter(item => item.route === "probe");
  let answered = (await probes()).length;
  for (;;) {
    await wait(400);
    const requests = await probes();
    if (requests.length <= answered) continue;
    answered = requests.length;
    let ids = [];
    try { ids = JSON.parse(requests[answered - 1].body).ids || []; } catch { /* probe all */ }
    const found = state().harnesses.filter(item => item.state !== "adapter_available").map(item => item.id);
    await emit(frame("delta", {event: {id: "e" + seq, type: "harness.changed", repo: "global", seq}, states: [entry(state(false, ids.length ? ids : found))]}));
    await wait(2000);
    await emit(frame("delta", {event: {id: "e" + seq, type: "harness.changed", repo: "global", seq}, states: [entry(state(true))]}));
  }
})();
