import { useState } from "react";
import { Button, Input, Link, Row, Select, Split, Stack, Text } from "@nscaledev/design-components/atoms";
import { FormField, Panel } from "@nscaledev/design-components/molecules";
import { PageHeader } from "@nscaledev/design-components/organisms";
import { DashboardTemplate } from "@nscaledev/design-patterns";
import { CURRENT_RELEASE, SOURCE_REPOSITORY, getDocumentSource } from "./docs-data.js";
import GuidedSetup from "./GuidedSetup.jsx";
import { agentPrompt, installerCommand, pluginCommands } from "./install.js";

const methods = [
  ["terminal", "Terminal"],
  ["agent", "Ask an agent"],
  ["plugin", "Plugin manager"],
];
const steps = [
  { title: "Task", heading: "Find the work", description: "Pick a task from the repository’s configured task provider.", command: "agentflow work" },
  { title: "Branch", heading: "Bind the branch", description: "Start the selected task. On the Agentflow repository, give its GitHub issue number.", command: "agentflow start '#123'" },
  { title: "Pull request", heading: "Check PR readiness", description: "Check readiness before opening the pull request. Carry the task reference into the review.", command: "agentflow pr --check" },
];

function CommandBlock({ command, label, hint }) {
  const [copyStatus, setCopyStatus] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopyStatus("Copied.");
    } catch {
      setCopyStatus("Select the text above and copy it manually.");
    }
  }
  return (
    <Stack gap={3}>
      <pre className="agentflow-code"><code>{command}</code></pre>
      <Row className="copy-actions" wrap gap={3}>
        <Button variant="primary" icon={copyStatus === "Copied." ? "check" : "copy"} onClick={copy}>{label}</Button>
        <Text role="status" tone="muted">{copyStatus}</Text>
      </Row>
      {hint && <Text as="p" tone="muted">{hint}</Text>}
    </Stack>
  );
}

function Installer() {
  const [method, setMethod] = useState("terminal");
  const [platform, setPlatform] = useState("posix");
  const [repoPath, setRepoPath] = useState("");
  const [project, setProject] = useState("");
  const [plugin, setPlugin] = useState("claude");
  const terminal = installerCommand(platform, repoPath);
  const prompt = agentPrompt(project);
  const commands = pluginCommands[plugin];

  return (
    <Panel className="installation-panel" title="Installation">
      <Stack gap={4}>
        <Row role="group" aria-label="Installation method" className="mode-control" wrap gap={0}>
          {methods.map(([id, label]) => (
            <Button key={id} variant="secondary" aria-pressed={id === method} onClick={() => setMethod(id)}>
              {label}
            </Button>
          ))}
        </Row>
        {method === "terminal" && (
          <Stack gap={3}>
            <div className="install-fields">
              <FormField label="Operating system">
                <Select value={platform} onChange={(event) => setPlatform(event.target.value)}>
                  <option value="posix">macOS / Linux · Bash</option>
                  <option value="windows">Windows · PowerShell</option>
                </Select>
              </FormField>
              <FormField label="Repository path (optional)">
                <Input mono value={repoPath} onChange={(event) => setRepoPath(event.target.value)}
                  autoComplete="off" maxLength={500}
                  placeholder={platform === "windows" ? "C:\\projects\\my-service" : "/path/to/repository"} />
              </FormField>
            </div>
            <Text as="p" tone="muted">Sign in to GitHub CLI with access to <strong>nscaledev/agentflow</strong>.</Text>
            <CommandBlock key={terminal} command={terminal} label="Copy command"
              hint="Run inside the target checkout or supply its path. An invalid checkout prompts for a replacement. Repeating setup preserves saved choices." />
            <details className="install-details">
              <summary>Release assets and setup guide</summary>
              <Stack gap={2}>
                <Text as="p">
                  See the <Link external href={getDocumentSource(CURRENT_RELEASE, "guides/project-installation")}>setup guide</Link> and{" "}
                  <Link external href={SOURCE_REPOSITORY + "/releases/tag/" + CURRENT_RELEASE}>release assets</Link>.
                  If the CLI is already installed, run this in the target checkout.
                </Text>
                <pre className="agentflow-code"><code>agentflow init</code></pre>
              </Stack>
            </details>
          </Stack>
        )}
        {method === "agent" && (
          <Stack gap={3}>
            <FormField label="Project name or path">
              <Input mono value={project} onChange={(event) => setProject(event.target.value)}
                placeholder="e.g. ~/work/my-service" autoComplete="off" maxLength={500} />
            </FormField>
            <CommandBlock key={prompt} command={prompt} label="Copy prompt"
              hint="Open your coding agent inside the target Git checkout and paste the prompt." />
          </Stack>
        )}
        {method === "plugin" && (
          <Stack gap={3}>
            <FormField label="Plugin client">
              <Select value={plugin} onChange={(event) => setPlugin(event.target.value)}>
                <option value="claude">Claude Code</option>
                <option value="codex">Codex CLI</option>
                <option value="gemini">Gemini CLI</option>
              </Select>
            </FormField>
            <Text as="p" tone="muted">Use your Git access to the internal Nscale repository. Open your client in the target checkout.</Text>
            <CommandBlock key={commands} command={commands} label="Copy plugin commands" />
            <Text as="p">
              {plugin === "claude" ? <>Run <code>/agentflow:manager install</code> in Claude Code.</> :
                <>Ask “Use the Agentflow manager to install Agentflow in this project.”</>}{" "}
              Use <code>update</code> to refresh it or <code>remove</code> to undo project setup.
            </Text>
          </Stack>
        )}
      </Stack>
    </Panel>
  );
}

function Workflow() {
  const [index, setIndex] = useState(0);
  const step = steps[index];
  return (
    <Panel title="From task to pull request">
      <Stack gap={4}>
        <Row role="group" aria-label="Choose a workflow step" className="mode-control" wrap gap={0}>
          {steps.map(({ title }, number) => (
            <Button key={title} variant="secondary" aria-pressed={number === index} onClick={() => setIndex(number)}>
              {title}
            </Button>
          ))}
        </Row>
        <Stack key={step.title} className="workflow-step" gap={2} aria-live="polite">
          <Text as="h4" variant="h3">{step.heading}</Text>
          <Text as="p" tone="muted">{step.description}</Text>
          <pre className="agentflow-code"><code>{step.command}</code></pre>
        </Stack>
      </Stack>
    </Panel>
  );
}

export default function Overview({ active }) {
  return (
    <section className="agentflow-overview" hidden={!active}>
      <DashboardTemplate header={
        <PageHeader title="Install Agentflow"
          description="Connect the work in your Git checkout to its task, engineering decisions, and pull request."
          actions={<Button to="#docs" variant="secondary">Explore the documentation</Button>}
          meta={<Text>Latest release <Link external href={SOURCE_REPOSITORY + "/releases/tag/" + CURRENT_RELEASE}>{CURRENT_RELEASE}</Link></Text>} />
      }>
        <Installer />
        <Split className="overview-panels" ratio="halves">
          <Workflow />
          <GuidedSetup />
        </Split>
        <Text as="p" variant="small" tone="muted">Engineering pilot. Consumer rollout awaits architecture approval.</Text>
      </DashboardTemplate>
    </section>
  );
}
