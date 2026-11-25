import { useEffect, useState } from "react";
import { Input, Link, Stack, Text } from "@nscaledev/design-components/atoms";
import { EmptyState, FormField, Panel } from "@nscaledev/design-components/molecules";
import { documents } from "virtual:agentflow-docs";
import { site } from "virtual:agentflow-release";
import "./docs.css";

export default function Docs({ id, anchor }) {
  const [query, setQuery] = useState("");
  const doc = documents.find(item => item.id === id) ?? documents[0];
  const terms = query.toLowerCase().match(/[\p{L}\p{N}_-]+/gu) ?? [];
  const results = documents.filter(item => terms.every(term => item.search.includes(term)));
  const groups = new Map();
  for (const item of results) {
    if (!groups.has(item.group)) groups.set(item.group, []);
    groups.get(item.group).push(item);
  }

  useEffect(() => {
    if (anchor) document.getElementById(anchor)?.scrollIntoView();
  }, [doc.id, anchor]);

  return (
    <div className="documentation">
      <header className="docs-heading">
        <Text as="h1" variant="h1">Documentation</Text>
        <Text as="p" tone="muted">{site.tag ? `Agentflow ${site.tag}` : "Agentflow cutover preview"}</Text>
      </header>
      <div className="docs-layout">
        <aside aria-label="Browse documentation">
          <Stack gap={3}>
            <FormField label="Search documentation">
              <Input type="search" value={query} onChange={event => setQuery(event.target.value)}
                aria-controls="docs-results" placeholder="Commands, providers, troubleshooting" />
            </FormField>
            <Text variant="small" tone="muted" role="status">{results.length} {results.length === 1 ? "document" : "documents"}</Text>
          </Stack>
          <nav id="docs-results" aria-label="Documentation pages">
            {[...groups].map(([group, items]) => (
              <section key={group} className="docs-group">
                <Text as="h2" variant="small" weight="bold">{group}</Text>
                {items.map(item => (
                  <Link key={item.id} href={`#docs/${item.id}`} aria-current={item.id === doc.id ? "page" : undefined}
                    onClick={() => setQuery("")}>{item.title}</Link>
                ))}
              </section>
            ))}
            {!results.length && <EmptyState icon="search" title="No matches">Try a command or a provider name.</EmptyState>}
          </nav>
        </aside>
        <Panel className="docs-reader">
          <article className="docs-prose" aria-labelledby="docs-title">
            <Text as="h2" variant="h2" id="docs-title">{doc.title}</Text>
            <div dangerouslySetInnerHTML={{ __html: doc.html }} />
            {doc.source && <footer><Link external href={doc.source}>View source on GitHub</Link></footer>}
          </article>
        </Panel>
      </div>
    </div>
  );
}
