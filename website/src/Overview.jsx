import { useState } from "react";
import { Button, Link, Row, Select, Stack, Text } from "@nscaledev/design-components/atoms";
import { FormField, Panel } from "@nscaledev/design-components/molecules";
import { site } from "virtual:agentflow-release";
import { installerCommand } from "./install.js";
import providersScreenshot from "./assets/console-providers.jpg";
import autonomyScreenshot from "./assets/console-autonomy.jpg";

const paths = [
  ["Enable a repository", "Choose providers, review generated changes, and verify your first pull request.", "#docs/enable"],
  ["Set up a workstation", "Install the CLI, connect your coding agent, and verify provider access.", "#docs/workstation"],
  ["See the workflow", "Follow a Jira issue from branch to release and a reviewed Confluence update.", "#demo"],
];

const capabilities = [
  ["Connect the tools you use", "Choose task, knowledge, and delivery providers per repository.", "#docs/compatibility"],
  ["Carry work through delivery", "Keep the task reference with the branch, commits, pull request, and release.", "#docs/jira-workflow"],
  ["Control what agents can do", "Inspect repository policy, review proposed effects, and approve consequential writes.", "#docs/commands"],
];

function ReleaseInstaller() {
  const [platform, setPlatform] = useState("posix");
  const [copyStatus, setCopyStatus] = useState("");
  if (!site.tag) return null;
  const command = installerCommand(platform, site.tag);
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopyStatus("Copied.");
    } catch {
      setCopyStatus("Select and copy the command above.");
    }
  }
  return (
    <Panel title="Install the verified release">
      <Stack gap={3}>
        <FormField label="Operating system">
          <Select value={platform} onChange={event => { setPlatform(event.target.value); setCopyStatus(""); }}>
            <option value="posix">macOS / Linux · Bash</option>
            <option value="windows">Windows · PowerShell</option>
          </Select>
        </FormField>
        <pre className="agentflow-code"><code>{command}</code></pre>
        <Row gap={2} wrap>
          <Button variant="secondary" onClick={copy}>Copy command</Button>
          <span role="status">{copyStatus}</span>
        </Row>
      </Stack>
    </Panel>
  );
}

export default function Overview() {
  return (
    <div className="launch-overview">
      <section className="launch-hero">
        <div>
          <Text as="p" className="launch-label" weight="bold">Agentflow · Nscale Engineering</Text>
          <h1>From task to release, with the same rules for people and agents.</h1>
          <Text as="p" className="launch-summary">Connect your Git checkout to engineering work, knowledge, and delivery. Review what will change before you hand the work off.</Text>
          <Row gap={3} wrap>
            <Button to="#docs/enable" variant="primary">Enable a repository</Button>
            <Button to="#docs/workstation" variant="secondary">Set up a workstation</Button>
            <Link href="#demo">See the workflow</Link>
          </Row>
        </div>
        <Panel title="A reviewed delivery workflow" className="launch-flow">
          <ol>
            <li><strong>Jira issue</strong><span>Bring the task and its criteria into your checkout.</span></li>
            <li><strong>Branch and policy</strong><span>Review a proposed commit or pull request.</span></li>
            <li><strong>GitHub release</strong><span>Verify the CI result and the published artifact.</span></li>
            <li><strong>Confluence update</strong><span>Publish a reviewed change against the current page revision.</span></li>
          </ol>
        </Panel>
      </section>
      <section className="launch-section" aria-labelledby="journeys">
        <Text as="h2" variant="h2" id="journeys">Get started</Text>
        <div className="launch-grid">
          {paths.map(([title, description, href]) => (
            <Panel key={href}>
              <Text as="h3" variant="h3"><Link href={href}>{title}</Link></Text>
              <Text as="p" tone="muted">{description}</Text>
            </Panel>
          ))}
        </div>
      </section>
      <section className="launch-section" aria-labelledby="capabilities">
        <Text as="h2" variant="h2" id="capabilities">How Agentflow works</Text>
        <div className="launch-grid">
          {capabilities.map(([title, description, href]) => (
            <Panel key={href}>
              <Text as="h3" variant="h3"><Link href={href}>{title}</Link></Text>
              <Text as="p" tone="muted">{description}</Text>
            </Panel>
          ))}
        </div>
      </section>
      <section className="launch-section" aria-labelledby="screenshots">
        <Text as="h2" variant="h2" id="screenshots">Inside the repository console</Text>
        <div className="launch-screens">
          <figure>
            <a href={providersScreenshot}>
              <img src={providersScreenshot} alt="Agentflow Providers screen showing Jira task management for the Atlas Service sandbox" width="1280" height="720" loading="lazy" />
            </a>
            <figcaption>Choose providers per repository and inspect where each setting comes from.</figcaption>
          </figure>
          <figure>
            <a href={autonomyScreenshot}>
              <img src={autonomyScreenshot} alt="Agentflow Autonomy screen showing effective approval rules for the Atlas Service sandbox" width="1280" height="720" loading="lazy" />
            </a>
            <figcaption>Review the effective rules for commits, pushes, and pull requests.</figcaption>
          </figure>
        </div>
        <Link href="#docs/console">Open the console guide</Link>
      </section>
      <section className="launch-section">
        <ReleaseInstaller />
        {site.tag ? <Link external href={site.repository + "/releases/tag/" + site.tag}>Release {site.tag}</Link> :
          <Text as="p" tone="muted">Preview build. Get signed installers from <Link external href={site.repository + "/releases/latest"}>Agentflow Releases</Link>.</Text>}
        <nav aria-label="More Agentflow resources" className="launch-links">
          <Link href="#docs/migration">Upgrade from Devflow</Link>
          <Link href="#docs/compatibility">Check compatibility</Link>
          <Link href="#docs/operations">Troubleshoot</Link>
          <Link href="#docs/contributing">Contribute</Link>
        </nav>
      </section>
    </div>
  );
}
