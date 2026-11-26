const concepts = [
  {
    id: "project-installation",
    since: "v0.44.0",
    type: "Guide",
    path: "guides/project-installation",
    title: "Install and manage Agentflow in a project",
    tags: "installation providers agents lifecycle install.sh install.ps1 manager",
    lead: "Install the verified release into a Git worktree from your terminal or coding agent.",
    body: [
      [
        "Choose the destination",
        "Run the command in the checkout or supply the project path. The installer asks again if the selected directory is outside a Git worktree.",
      ],
      [
        "Select providers",
        "Interactive setup shows repository evidence, lets you change suggestions, and saves confirmed choices in the project.",
      ],
      [
        "Use an agent",
        "Ask the Agentflow manager to install Agentflow in the checkout. It can also update the verified binary and workflow pin or preview removal.",
      ],
    ],
  },
  {
    id: "task-workflow",
    type: "Guide",
    path: "guides/task-workflow",
    title: "Work on a Agentflow task",
    tags: "tasks github contributor",
    lead: "Carry a GitHub issue from task selection through a reviewed change in the Agentflow repository.",
    body: [
      [
        "Check the configured providers",
        "Inspect the resolved settings. Agentflow’s own checkout uses GitHub Issues for tasks and Repository for knowledge.",
      ],
      ["$", "agentflow setup providers --explain"],
      [
        "Bind the task",
        "Pick an issue with agentflow work, then start it with its issue number. Include the qualified task reference when you open the pull request.",
      ],
      ["$", "agentflow start '#123'"],
    ],
  },
  {
    id: "provider-architecture",
    type: "Decision",
    path: "decisions/provider-architecture",
    title: "Provider contracts and repository configuration",
    tags: "architecture providers",
    lead: "Teams can select services independently while keeping the same CLI and MCP operations.",
    body: [
      [
        "Decision",
        "Agentflow translates its typed task and knowledge objects into the APIs or files of each configured provider. Qualified references preserve identity across branches, commits, and pull requests.",
      ],
      [
        "Pilot",
        "GitHub Issues and Repository knowledge serve the Agentflow pilot. Other repositories can retain Jira and Confluence selections.",
      ],
    ],
  },
  {
    id: "knowledge-publication",
    type: "Runbook",
    path: "runbooks/knowledge-publication",
    title: "Publish a knowledge document",
    tags: "knowledge okf publication",
    lead: "Publish a revision-checked concept through a reviewed pull request.",
    body: [
      [
        "Read the existing revision",
        "Open the qualified reference and capture its revision before preparing an update.",
      ],
      ["$", "agentflow docs get REF --json"],
      [
        "Review and publish",
        "Supply the expected revision and an operation ID. Review the preview, approve the request, then reopen the document after the publication PR merges.",
      ],
    ],
  },
];
const conflict = [
  "Diagnose publication conflicts",
  "For a stale revision, reread the document and compare it with the draft. If replay returns an existing PR, check its head and review state. Check duplicate references for two files with the same x-agentflow-ref.",
];
export const SOURCE_REPOSITORY = "https://github.com/ryanhelms/agentflow";

// Release order determines the Latest alias and which guides existed at each tag.
const releaseCommits = {
  "v0.46.0": "5d62691af79f0dae515ff5445925ab4ca3d7830f",
  "v0.45.0": "aa76d13088106968c4f2b64d5ed1210bf49191a6",
  "v0.44.1": "fa0dd962483bd2b6a14d14606111a9985d15e612",
  "v0.44.0": "f4bb696e16c781713cbcc522ce0010fec9371d17",
  "v0.43.0": "484f0a7c30b9c72ad3c54ef990a2e60674ee43a2",
  "v0.42.0": "3747fff22b9b4f8e3227282ad177b4c103bfc603",
  "v0.41.0": "4611502d7bec9ff156536db36ed2cc1332fe3b06",
  "v0.40.0": "bf5023bc057616c9650122e56be4830fe6a069fe",
};

export const RELEASES = Object.keys(releaseCommits);
export const CURRENT_RELEASE = RELEASES[0];

export function getDocumentSections(document, version) {
  if (document.id === "knowledge-publication" && version !== "v0.40.0") {
    return [...document.body, conflict];
  }
  return document.body;
}

export function getDocumentSource(version, path) {
  return `${SOURCE_REPOSITORY}/blob/${releaseCommits[version]}/docs/knowledge/engineering/${path}.md`;
}

export function matchDocuments(version, query) {
  const releaseIndex = RELEASES.indexOf(version);
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);

  return concepts.filter((document) => {
    if (document.since && releaseIndex > RELEASES.indexOf(document.since)) {
      return false;
    }
    const sections = getDocumentSections(document, version);
    const content = [document.title, document.tags, document.type, document.lead, ...sections.flat()];
    const text = content.join(" ").toLowerCase();
    return words.every((word) => text.includes(word));
  });
}
