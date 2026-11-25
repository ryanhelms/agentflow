import { useState } from "react";
import { Button, Link, Row, Stack, Text } from "@nscaledev/design-components/atoms";
import GuidedSetup from "./GuidedSetup.jsx";
import { Panel } from "@nscaledev/design-components/molecules";

const steps = [
  {
    title: "Open the task",
    description: "Load a sandbox Jira issue and its acceptance criteria. Check the selected providers before starting work.",
    command: "agentflow context LAUNCH-42",
    agent: "Read the task context for LAUNCH-42 and summarize the acceptance criteria.",
    result: "LAUNCH-42 · Make export status visible · 2 acceptance criteria",
  },
  {
    title: "Bind the branch",
    description: "Review the project, issue, and any configured start transition before accepting it.",
    command: "agentflow start LAUNCH-42",
    agent: "Prepare to start LAUNCH-42. Show me the branch and any Jira transition before applying it.",
    result: "Proposed task: LAUNCH-42 · Target checkout: atlas-service",
  },
  {
    title: "Review the PR",
    description: "Run readiness checks and inspect the proposed title, files, and publication destination.",
    command: "agentflow pr --check\nagentflow pr --draft --prepare",
    agent: "Check PR readiness and prepare a draft. Show the target repository and wait for approval.",
    result: "Draft proposal · LAUNCH-42 · Repository approval required",
  },
  {
    title: "Inspect the release",
    description: "In the sandbox, an engineer merges the reviewed PR and the configured workflow publishes the release. Inspect the release and its artifacts.",
    command: "agentflow release get v1.4.0",
    agent: "Inspect the sandbox release v1.4.0. Show its revision and published artifacts.",
    result: "atlas-service v1.4.0 · Release revision and artifacts available",
  },
  {
    title: "Update Confluence",
    description: "Read the current page revision, review the proposed text, and approve the update. Read the page afterward to confirm it.",
    command: "agentflow docs get confluence/LAUNCH/42",
    agent: "Read the release notes page. Prepare a Confluence update against its current revision and wait for approval.",
    result: "Release notes · Current revision 7 · Review the proposed update",
  },
];

export default function Demo() {
  const [step, setStep] = useState(0);
  const [client, setClient] = useState("Terminal");
  const current = steps[step];
  return (
    <div className="launch-overview">
      <section className="launch-section">
        <h1>Follow a task from Jira to release</h1>
        <Text as="p" tone="muted">Sample data from a fictional Atlas Service sandbox. This walkthrough runs in your browser.</Text>
        <Stack gap={4}>
          <Row role="group" aria-label="Choose a client" wrap gap={2} className="mode-control">
            {["Terminal", "Claude Code Desktop", "Codex"].map(name => (
              <Button key={name} variant="secondary" aria-pressed={name === client} onClick={() => setClient(name)}>{name}</Button>
            ))}
          </Row>
          <ol className="demo-steps" aria-label="Workflow steps">
            {steps.map(({ title }, i) => <li key={title}>
              <Button variant="secondary" aria-current={i === step ? "step" : undefined}
                aria-pressed={i === step} onClick={() => setStep(i)}>{title}</Button>
            </li>)}
          </ol>
          <Panel title={current.title}>
            <Stack gap={3} aria-live="polite">
              <Text as="p">{current.description}</Text>
              <pre className="agentflow-code"><code>{client === "Terminal" ? current.command : current.agent}</code></pre>
              {client !== "Terminal" && <Text as="p" tone="muted">Provider availability depends on your client and sign-in. Use the terminal path for a step your client delegates.</Text>}
              <Text as="p"><strong>Example result</strong> · {current.result}</Text>
            </Stack>
          </Panel>
          <Row gap={2} wrap>
            <Button variant="secondary" disabled={step === 0} onClick={() => setStep(step - 1)}>Previous</Button>
            <Button variant="primary" disabled={step === steps.length - 1} onClick={() => setStep(step + 1)}>Next</Button>
          </Row>
          <Link href="#docs/jira-workflow">Read the sandbox runbook</Link>
          <GuidedSetup />
        </Stack>
      </section>
    </div>
  );
}
