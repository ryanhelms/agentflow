import { useState } from "react";
import { Icon, Input, Link, Row, Select, Stack, Text } from "@agentflow/design-components/atoms";
import { EmptyState, FormField, ListRow, Panel } from "@agentflow/design-components/molecules";
import { PageHeader } from "@agentflow/design-components/organisms";
import { WorkbenchTemplate } from "@agentflow/design-patterns";
import {
  CURRENT_RELEASE,
  RELEASES,
  getDocumentSections,
  getDocumentSource,
  matchDocuments,
} from "./docs-data.js";
import "./docs.css";

const releasePreferenceKey = "agentflow.site-study.docs.version";

function preferredRelease() {
  try {
    const saved = localStorage.getItem(releasePreferenceKey);
    if (RELEASES.includes(saved)) return saved;
  } catch {
    // The current tab can still use its own selection.
  }
  return "latest";
}

function DocumentArticle({ document, version }) {
  if (!document) {
    return (
      <EmptyState icon="search" title="No matches">
        Try a command, a document title, or a shorter search.
      </EmptyState>
    );
  }

  const sections = getDocumentSections(document, version);
  return (
    <Panel
      className="docs-reader"
      footer={
        <Row wrap justify="between" className="docs-reader-footer">
          <Link external href={getDocumentSource(version, document.path)}>
            Read the full source at {version}
          </Link>
          <Text variant="small" tone="muted">{document.type} · Engineering · OKF 0.3</Text>
        </Row>
      }
    >
      <Stack gap={5} className="docs-prose rise">
        <Stack gap={2}>
          <Text as="h2" variant="h2">{document.title}</Text>
          <Text as="p" tone="muted">{document.lead}</Text>
        </Stack>
        {sections.map(([heading, paragraph]) =>
          heading === "$" ? (
            <pre key={paragraph} className="docs-command"><code>{paragraph}</code></pre>
          ) : (
            <Stack as="section" gap={2} key={heading}>
              <Text as="h3" variant="h3">{heading}</Text>
              <Text as="p">{paragraph}</Text>
            </Stack>
          ),
        )}
      </Stack>
    </Panel>
  );
}

export default function Docs({ active }) {
  const [preference, setPreference] = useState(preferredRelease);
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState("task-workflow");
  const version = preference === "latest" ? CURRENT_RELEASE : preference;
  const results = matchDocuments(version, query);
  const document = results.find(({ id }) => id === selected) ?? results[0];

  function selectMatchingDocument(nextVersion, nextQuery) {
    const nextResults = matchDocuments(nextVersion, nextQuery);
    if (nextResults.length && !nextResults.some(({ id }) => id === selected)) {
      setSelected(nextResults[0].id);
    }
  }

  function changeRelease(event) {
    const value = event.target.value;
    setPreference(value);
    selectMatchingDocument(value === "latest" ? CURRENT_RELEASE : value, query);
    try {
      localStorage.setItem(releasePreferenceKey, value);
    } catch {
      // The selection is still active in the current tab.
    }
  }

  function changeQuery(event) {
    const value = event.target.value;
    setQuery(value);
    selectMatchingDocument(version, value);
  }

  return (
    <section className="docs-page" data-page="docs" hidden={!active} aria-label="Documentation">
      <WorkbenchTemplate
        leftWidth={312}
        header={
          <PageHeader
            title="Agentflow documentation"
            description="Search guides, decisions, and runbooks for your release."
            actions={
              <FormField label="Agentflow release">
                <Select value={preference} onChange={changeRelease}>
                  <option value="latest">Latest ({CURRENT_RELEASE})</option>
                  {RELEASES.map((tag) => <option key={tag} value={tag}>{tag}</option>)}
                </Select>
              </FormField>
            }
          />
        }
        left={
          <aside aria-label="Engineering documents">
            <Stack className="docs-search" gap={3}>
              <FormField label="Search documentation">
                <Input
                  type="search"
                  placeholder="Search this release"
                  autoComplete="off"
                  value={query}
                  onChange={changeQuery}
                />
              </FormField>
              <Text variant="small" tone="muted" aria-live="polite">
                {results.length} {results.length === 1 ? "document" : "documents"}
              </Text>
            </Stack>
            {results.map((result) => (
              <ListRow
                key={result.id}
                className="docs-result"
                onClick={() => setSelected(result.id)}
                selected={result.id === document?.id}
                aria-current={result.id === document?.id ? "page" : undefined}
              >
                <Icon name="audit" size={16} />
                <Text>{result.title}</Text>
              </ListRow>
            ))}
          </aside>
        }
      >
        <article className="docs-article" aria-live="polite">
          <DocumentArticle key={document?.id} document={document} version={version} />
        </article>
      </WorkbenchTemplate>
    </section>
  );
}
