import { useState } from "react";
import { Button, Row, Select, Stack, Text } from "@nscaledev/design-components/atoms";
import { FormField, Panel } from "@nscaledev/design-components/molecules";

const preferenceKey = "agentflow.site-study.fixture.atlas-service.providers.v1";
const taskOptions = [
  ["jira", "Jira"],
  ["github-issues", "GitHub Issues"],
  ["asana", "Asana"],
  ["filesystem", "FileSystem · local files"],
  ["repository", "Repository · Git-backed files"],
  ["none", "None"],
];
const knowledgeOptions = [
  ["repository", "Repository · Git-backed files"],
  ["filesystem", "FileSystem · local files"],
  ["confluence", "Confluence"],
  ["github-wiki", "GitHub Wiki"],
  ["none", "None · Skip this role"],
];

function loadProviders() {
  try {
    const saved = JSON.parse(localStorage.getItem(preferenceKey));
    if (Array.isArray(saved) && saved.length === 2 &&
      taskOptions.some(([id]) => saved[0] === id) &&
      knowledgeOptions.some(([id]) => saved[1] === id)) {
      return saved;
    }
  } catch {}
  return null;
}

const describe = ([task, knowledge]) => "Task provider " + task + " · Knowledge provider " + knowledge + ".";

export default function GuidedSetup() {
  const [applied, setApplied] = useState(loadProviders);
  const [pair, setPair] = useState(() => applied ?? ["jira", ""]);
  const [phase, setPhase] = useState(() => applied ? "saved" : "choose");
  const [message, setMessage] = useState(() => applied ? "Already configured. Using saved choices. " + describe(applied) : "");
  const [task, knowledge] = pair;

  function edit() {
    setPhase("choose");
    setMessage("Adjust the choices and preview the changes.");
  }

  function advance() {
    if (phase === "preview") {
      try {
        localStorage.setItem(preferenceKey, JSON.stringify(pair));
      } catch {}
      setApplied(pair);
      setPhase("saved");
      setMessage("Saved to the sample checkout. " + describe(pair));
    } else if (applied && pair.every((value, index) => value === applied[index])) {
      setPhase("saved");
      setMessage("Already configured. Both choices retained. No changes. " + describe(pair));
    } else {
      setPhase("preview");
      setMessage("Review these provider IDs for .agentflow/config.yml. " + describe(pair));
    }
  }

  return (
    <Panel title="Try guided setup">
      <Stack gap={3}>
        <Text as="p" tone="muted">Try provider selection in the sample <code>atlas-service</code> checkout.</Text>
        {phase === "choose" && !applied && (
          <Text as="p" tone="muted">
            The sample <code>.agentflow/config.yml</code> lists <code>ABC</code> under{" "}
            <code>jira.projects</code>. Setup suggests Jira and asks where the team keeps knowledge.
          </Text>
        )}
        {phase !== "saved" && (
          <div className="guide-fields">
            <FormField label="Task management">
              <Select value={task} disabled={phase !== "choose"} onChange={(event) => setPair([event.target.value, knowledge])}>
                {taskOptions.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
              </Select>
            </FormField>
            <FormField label="Knowledge base">
              <Select value={knowledge} disabled={phase !== "choose"} onChange={(event) => setPair([task, event.target.value])}>
                <option value="" disabled>Choose a source…</option>
                {knowledgeOptions.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
              </Select>
            </FormField>
          </div>
        )}
        <Text as="p" role="status" tone="muted" hidden={!message}>{message}</Text>
        <Row wrap gap={2}>
          <Button onClick={advance} disabled={phase === "choose" && !knowledge}>
            {{ choose: "Preview setup", preview: "Confirm in preview", saved: "Run preview again" }[phase]}
          </Button>
          {phase !== "choose" && <Button variant="ghost" onClick={edit}>Edit choices</Button>}
        </Row>
      </Stack>
    </Panel>
  );
}
