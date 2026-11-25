import { readFileSync, statSync } from "node:fs";
import { join, posix } from "node:path";
import MarkdownIt from "markdown-it";

export const catalog = [
  ["enable", "Get started", "Enable a repository", "docs/launch/enable.md"],
  ["workstation", "Get started", "Set up a workstation", "docs/launch/workstation.md"],
  ["migration", "Get started", "Upgrade from Devflow", "docs/agentflow-migration.md"],
  ["jira-workflow", "Workflows", "Jira and Confluence workflow", "docs/launch/jira-confluence.md"],
  ["github-workflow", "Workflows", "GitHub Issues workflow", "docs/knowledge/engineering/guides/task-workflow.md"],
  ["knowledge", "Workflows", "Publish a knowledge document", "docs/knowledge/engineering/runbooks/knowledge-publication.md"],
  ["console", "Workflows", "Repository console", "docs/repository-console.md"],
  ["compatibility", "Reference", "Client and provider compatibility", "docs/launch/compatibility.md"],
  ["commands", "Reference", "CLI and MCP reference", "docs/launch/reference.md"],
  ["providers", "Reference", "Provider configuration", "docs/providers.md"],
  ["clients", "Reference", "Client plugins", "docs/client-plugins.md"],
  ["operations", "Operate", "Operations and troubleshooting", "docs/operations.md"],
  ["releases", "Operate", "Version and release a repository", "docs/releases.md"],
  ["notifications", "Operate", "Notifications and replay", "docs/notifications.md"],
  ["secops", "Operate", "Security triage", "docs/secops.md"],
  ["security", "Operate", "Security policy and reporting", "SECURITY.md"],
  ["architecture", "Build Agentflow", "Architecture and maintenance", "docs/launch/maintainer.md"],
  ["provider-authoring", "Build Agentflow", "Author a provider", "docs/provider-authoring.md"],
  ["definitions", "Build Agentflow", "Provider definition design", "docs/design/provider-definitions.md"],
  ["contributing", "Build Agentflow", "Contribute to Agentflow", "CONTRIBUTING.md"],
  ["product-audit", "Build Agentflow", "Feature inventory and history", "docs/launch/feature-inventory.md"],
  ["changelog", "Build Agentflow", "Release history", "CHANGELOG.md"],
  ["project", "Build Agentflow", "Repository README", "README.md"],
];

const documentByPath = new Map(catalog.map(([id, , , path]) => [path, id]));

export function releaseMetadata(env, changelog) {
  const repository = env.AGENTFLOW_SITE_REPOSITORY || "nscaledev/agentflow";
  const docsRef = env.AGENTFLOW_SITE_DOCS_REF || "";
  const tag = env.AGENTFLOW_SITE_RELEASE || "";
  const revision = env.AGENTFLOW_SITE_RELEASE_SHA || "";
  if (!/^nscaledev\/(devflow|agentflow)$/.test(repository)) throw new Error("invalid website repository");
  if (docsRef && !/^[a-f0-9]{40}$/.test(docsRef)) throw new Error("invalid website documentation ref");
  if (tag || revision) {
    if (repository !== "nscaledev/agentflow" || !/^v\d+\.\d+\.\d+$/.test(tag) || !/^[a-f0-9]{40}$/.test(revision)) {
      throw new Error("website downloads require the canonical repository, a release tag, and full release SHA");
    }
  }
  const tags = [...new Set(Array.from(changelog.matchAll(/^## \[v?(\d+\.\d+\.\d+)\]/gm), match => "v" + match[1]))];
  if (tag && !tags.includes(tag)) throw new Error("website release is missing from the changelog");
  return { repository: `https://github.com/${repository}`, repositorySlug: repository, docsRef, tag, revision, tags };
}

function localLink(href, document, site, isDirectory) {
  if (/^[a-z][a-z\d+.-]*:/i.test(href) || href.startsWith("//")) return href;
  const [path, fragment] = href.split("#", 2);
  if (!path) return `#docs/${document.id}${fragment ? "/" + fragment : ""}`;
  const relative = posix.normalize(posix.join(posix.dirname(document.path), path));
  const target = documentByPath.get(relative);
  if (target) return `#docs/${target}${fragment ? "/" + fragment : ""}`;
  const kind = isDirectory(relative) ? "tree" : "blob";
  return `${site.repository}/${kind}/${site.docsRef || "main"}/${relative}${fragment ? "#" + fragment : ""}`;
}

function headingID(title) {
  return title.toLowerCase().trim().replace(/[^\p{L}\p{N} _-]/gu, "").replace(/ /g, "-");
}

export function renderDocument(source, document, site, isDirectory = () => false) {
  const markdown = new MarkdownIt({ html: false });
  const renderLink = markdown.renderer.rules.link_open;
  markdown.renderer.rules.link_open = (tokens, i, options, env, renderer) => {
    const href = tokens[i].attrGet("href");
    if (href) tokens[i].attrSet("href", localLink(href, document, site, isDirectory));
    return renderLink ? renderLink(tokens, i, options, env, renderer) : renderer.renderToken(tokens, i, options);
  };
  const renderHeading = (tokens, i, options, env, renderer) => {
    const base = headingID(tokens[i + 1].content) || "section";
    const count = env.headings.get(base) ?? 0;
    tokens[i].attrSet("id", count ? `${base}-${count}` : base);
    env.headings.set(base, count + 1);
    return renderer.renderToken(tokens, i, options);
  };
  markdown.renderer.rules.heading_open = renderHeading;
  const text = source.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n/, "");
  const body = text.replace(/^# .+\r?\n/, "");
  return {
    ...document,
    html: markdown.render(body, { headings: new Map() }),
    search: [...new Set(`${document.title} ${document.group} ${text}`.toLowerCase().match(/[\p{L}\p{N}_-]+/gu))].join(" "),
    source: site.docsRef ? `${site.repository}/blob/${site.docsRef}/${document.path}` : null,
  };
}

export function contentFor(root, env) {
  const read = path => readFileSync(join(root, path), "utf8");
  const site = releaseMetadata(env, read("CHANGELOG.md"));
  const isDirectory = path => !path.startsWith("../") &&
    Boolean(statSync(join(root, path), { throwIfNoEntry: false })?.isDirectory());
  return {
    site,
    documents: catalog.map(([id, group, title, path]) => renderDocument(read(path), { id, group, title, path }, site, isDirectory)),
  };
}
