import "./style.css";

const root = document.getElementById("df-study");
const find = (s) => root.querySelector(s),
  findAll = (s) => Array.from(root.querySelectorAll(s));
const os = find("#df-os"),
  repoPath = find("#df-repo-path"),
  plugin = find("#df-plugin");
const project = find("#df-project"),
  search = find(".df-find input"),
  release = find(".df-docbar select");
let copyAttempt = 0;
function resetCopy() {
  copyAttempt++;
  findAll(".df-message").forEach((n) => (n.textContent = ""));
}
function updateInstall() {
  const windows = os.value === "windows",
    path = repoPath.value;
  repoPath.placeholder = windows ? "C:\\projects\\my-service" : "/path/to/repository";
  const shellArg = "'" + path.split("'").join("'\u0022'\u0022'") + "'";
  const psArg = "'" + path.replace(/['\u2018\u2019\u201A\u201B]/g, (c) => "'" + c) + "'";
  const flag = path ? (windows ? " -Repo " + psArg : " --repo " + shellArg) : "";
  const start =
    '$s = gh release download -R nscaledev/devflow -p install.ps1 -O -; if ($LASTEXITCODE -eq 0) { & ([scriptblock]::Create($s -join "`n"))';
  find("#df-install-text").textContent = windows
    ? start + flag + " }"
    : 's=$(gh release download -R nscaledev/devflow -p install.sh -O -) && bash -c "$s" devflow' +
      flag;
  resetCopy();
}
function updatePlugin() {
  const choices = {
    claude: [
      "claude plugin marketplace add nscaledev/devflow",
      "claude plugin install devflow@nscale",
    ],
    codex: ["codex plugin marketplace add nscaledev/devflow", "codex plugin add devflow@nscale"],
    gemini: ["gemini extensions install https://github.com/nscaledev/devflow"],
  };
  find("#df-plugin-note").textContent =
    "Use your Git access to the internal Nscale repository. Open your client in the target checkout.";
  find("#df-manager-use").textContent =
    (plugin.value === "claude"
      ? "Run /devflow:manager install in Claude Code."
      : "With the manager installed, ask “Use the Devflow manager to install Devflow in this project.”") +
    " Use update to refresh it or remove to undo project setup.";
  find("#df-plugin-text").textContent = choices[plugin.value].join("\n");
  resetCopy();
}
os.onchange = updateInstall;
repoPath.oninput = updateInstall;
plugin.onchange = updatePlugin;
updateInstall();
updatePlugin();
function updatePrompt() {
  const name = project.value.trim();
  find("#df-agent-text").textContent =
    "Install Devflow from https://github.com/nscaledev/devflow in " +
    (name ? "the project " + JSON.stringify(name) : "this project") +
    ". Reuse saved provider choices. For any unconfigured role, suggest a provider when project evidence is clear and ask me when it is ambiguous. Preview changes before applying them, then verify setup and show me what changed.";
  resetCopy();
}
project.oninput = updatePrompt;
updatePrompt();
const taskProvider = find("#df-task"),
  knowledgeProvider = find("#df-knowledge");
const guideStep = find("[data-guide-step]"),
  guideEdit = find("[data-guide-edit]"),
  guideMessage = find(".df-guide-state");
const fixtureKey = "devflow.site-study.fixture.atlas-service.providers.v1";
let guidePhase = "choose",
  applied;
const describe = (pair) => "Task provider " + pair[0] + " · Knowledge provider " + pair[1] + ".";
function showGuide(phase, message) {
  guidePhase = phase;
  guideMessage.textContent = message;
  taskProvider.disabled = knowledgeProvider.disabled = phase !== "choose";
  find(".df-guide-fields").hidden = phase === "saved";
  find("[data-guide-evidence]").hidden = phase !== "choose" || !!applied;
  guideEdit.hidden = phase === "choose";
  guideStep.disabled = phase === "choose" && !knowledgeProvider.value;
  guideStep.textContent = {
    choose: "Preview setup",
    preview: "Confirm in preview",
    saved: "Run preview again",
  }[phase];
}
function validOption(field, value) {
  return (
    typeof value === "string" &&
    value !== "" &&
    Array.from(field.options).some((option) => option.value === value)
  );
}
try {
  const ids = JSON.parse(localStorage.getItem(fixtureKey));
  if (
    Array.isArray(ids) &&
    ids.length === 2 &&
    validOption(taskProvider, ids[0]) &&
    validOption(knowledgeProvider, ids[1])
  ) {
    applied = ids;
    taskProvider.value = ids[0];
    knowledgeProvider.value = ids[1];
  }
} catch {}
showGuide(
  applied ? "saved" : "choose",
  applied ? "Already configured. Using saved choices. " + describe(applied) : "",
);
knowledgeProvider.onchange = () => {
  guideStep.disabled = !knowledgeProvider.value;
};
guideStep.onclick = () => {
  const pair = [taskProvider.value, knowledgeProvider.value];
  const description = describe(pair);
  if (guidePhase === "preview") {
    applied = pair;
    try {
      localStorage.setItem(fixtureKey, JSON.stringify(pair));
    } catch {}
    showGuide("saved", "Saved to the sample checkout. " + description);
  } else if (applied && pair.every((value, i) => value === applied[i])) {
    showGuide("saved", "Already configured. Both choices retained. No changes. " + description);
  } else {
    showGuide("preview", "Review these provider IDs for .devflow/config.yml. " + description);
  }
};
guideEdit.onclick = () => {
  showGuide("choose", "Adjust the choices and preview the changes.");
  taskProvider.focus();
};
findAll("[data-mode]").forEach(
  (button) =>
    (button.onclick = () => {
      findAll("[data-mode]").forEach((b) => b.setAttribute("aria-pressed", String(b === button)));
      findAll("[data-method]").forEach(
        (p) => (p.hidden = p.dataset.method !== button.dataset.mode),
      );
      resetCopy();
    }),
);
findAll("[data-copy]").forEach(
  (button) =>
    (button.onclick = async () => {
      resetCopy();
      const attempt = copyAttempt,
        status = button.nextElementSibling;
      const value = document.getElementById(button.dataset.copy).textContent;
      try {
        await navigator.clipboard.writeText(value);
        if (attempt === copyAttempt) status.textContent = "Copied.";
      } catch {
        if (attempt === copyAttempt)
          status.textContent = "Select the text above and copy it manually.";
      }
    }),
);
const stages = [
  ["Find the work", "Pick a task from the repository’s configured task provider.", "devflow work"],
  [
    "Bind the branch",
    "Start the selected task. On the Devflow repository, give its GitHub issue number.",
    "devflow start '#123'",
  ],
  [
    "Check PR readiness",
    "Check readiness before opening the pull request. Carry the task reference into the review.",
    "devflow pr --check",
  ],
];
const concepts = [
  {
    id: "project-installation",
    since: "v0.44.0",
    type: "Guide",
    path: "guides/project-installation",
    title: "Install and manage Devflow in a project",
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
        "Ask the Devflow manager to install Devflow in the checkout. It can also update the verified binary and workflow pin or preview removal.",
      ],
    ],
  },
  {
    id: "task-workflow",
    type: "Guide",
    path: "guides/task-workflow",
    title: "Work on a Devflow task",
    tags: "tasks github contributor",
    lead: "Carry a GitHub issue from task selection through a reviewed change in the Devflow repository.",
    body: [
      [
        "Check the configured providers",
        "Inspect the resolved settings. Devflow’s own checkout uses GitHub Issues for tasks and Repository for knowledge.",
      ],
      ["$", "devflow setup providers --explain"],
      [
        "Bind the task",
        "Pick an issue with devflow work, then start it with its issue number. Include the qualified task reference when you open the pull request.",
      ],
      ["$", "devflow start '#123'"],
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
        "Devflow translates its typed task and knowledge objects into the APIs or files of each configured provider. Qualified references preserve identity across branches, commits, and pull requests.",
      ],
      [
        "Pilot",
        "GitHub Issues and Repository knowledge serve the Devflow pilot. Other repositories can retain Jira and Confluence selections.",
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
      ["$", "devflow docs get REF --json"],
      [
        "Review and publish",
        "Supply the expected revision and an operation ID. Review the preview, approve the request, then reopen the document after the publication PR merges.",
      ],
    ],
  },
];
const conflict = [
  "Diagnose publication conflicts",
  "For a stale revision, reread the document and compare it with the draft. If replay returns an existing PR, check its head and review state. Check duplicate references for two files with the same x-devflow-ref.",
];
// Release order determines the Latest alias and which guides existed at each tag.
const releaseCommits = {
  "v0.44.1": "fa0dd962483bd2b6a14d14606111a9985d15e612",
  "v0.44.0": "f4bb696e16c781713cbcc522ce0010fec9371d17",
  "v0.43.0": "484f0a7",
  "v0.42.0": "3747fff",
  "v0.41.0": "4611502",
  "v0.40.0": "bf5023b",
};
const releaseOrder = Object.keys(releaseCommits);
const currentRelease = releaseOrder[0];
const releasePreferenceKey = "devflow.site-study.docs.version";
release.replaceChildren(
  new Option("Latest (" + currentRelease + ")", "latest"),
  ...releaseOrder.map((tag) => new Option(tag, tag)),
);

let pref = "latest",
  selected = import.meta.hot?.data.selectedDocument ?? "task-workflow";
function valid(x) {
  return x === "latest" || Object.hasOwn(releaseCommits, x);
}
try {
  const saved = localStorage.getItem(releasePreferenceKey);
  if (valid(saved)) pref = saved;
} catch {}

const version = () => (pref === "latest" ? currentRelease : pref);
const body = (d) =>
  d.id === "knowledge-publication" && version() !== "v0.40.0" ? [...d.body, conflict] : d.body;
function el(tag, txt, cls) {
  const item = document.createElement(tag);
  if (txt !== undefined) item.textContent = txt;
  if (cls) item.className = cls;
  return item;
}
function page(which) {
  findAll("[data-page]").forEach((n) => (n.hidden = n.dataset.page !== which));
  findAll("nav [data-nav]").forEach((n) => {
    if (n.dataset.nav === which) n.setAttribute("aria-current", "page");
    else n.removeAttribute("aria-current");
  });
}
findAll("[data-nav]").forEach((n) => (n.onclick = () => page(n.dataset.nav)));
findAll("[data-step]").forEach(
  (n) =>
    (n.onclick = () => {
      findAll("[data-step]").forEach((b) => b.setAttribute("aria-pressed", String(b === n)));
      const [title, blurb, cmd] = stages[Number(n.dataset.step)];
      const section = find(".df-story");
      section.replaceChildren(el("h3", title), el("p", blurb), el("code", cmd, "df-command"));
      section.classList.remove("df-move");
      void section.offsetWidth;
      section.classList.add("df-move");
    }),
);
function drawDocs() {
  release.value = pref;

  const available = concepts.filter(
    (d) => !d.since || releaseOrder.indexOf(version()) <= releaseOrder.indexOf(d.since),
  );
  const words = search.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const results = available.filter((d) => {
    const hay = [d.title, d.tags, d.type, d.lead, ...body(d).flat()].join(" ").toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  const found = results.find((d) => d.id === selected) || results[0];
  find(".df-count").textContent =
    results.length === 1 ? "1 document" : results.length + " documents";
  const list = find(".df-results"),
    article = find("article");
  list.replaceChildren();
  article.replaceChildren();
  if (!found) {
    article.append(
      el("h2", "No matches"),
      el("p", "Try a command, a document title, or a shorter search."),
    );
    return;
  }
  selected = found.id;
  results.forEach((d) => {
    const btn = el("button", d.title);
    btn.type = "button";
    if (d === found) btn.setAttribute("aria-current", "page");
    btn.onclick = () => {
      selected = d.id;
      drawDocs();
      Array.from(list.children)
        .find((b) => b.textContent === d.title)
        ?.focus();
    };
    list.append(btn);
  });
  article.append(el("h2", found.title), el("p", found.lead));
  for (const [heading, paragraph] of body(found)) {
    if (heading === "$") article.append(el("code", paragraph));
    else article.append(el("h3", heading), el("p", paragraph));
  }
  const a = el("a", "Read the full source at " + version(), "df-sourcelink");
  a.href =
    "https://github.com/nscaledev/devflow/blob/" +
    releaseCommits[version()] +
    "/docs/knowledge/engineering/" +
    found.path +
    ".md";
  a.target = "_blank";
  a.rel = "noopener";
  const footer = el("footer");
  footer.className = "df-doc-source";
  footer.append(a, el("div", found.type + " · Engineering · OKF 0.3", "df-meta"));
  article.append(footer);
}
search.oninput = drawDocs;
release.onchange = (e) => {
  pref = e.target.value;
  try {
    localStorage.setItem(releasePreferenceKey, pref);
  } catch {}
  drawDocs();
};
drawDocs();

// Vite re-evaluates this module in place. DOM event properties above replace the
// previous handlers while input values, the active tab and the scroll position stay put.
if (import.meta.hot) {
  import.meta.hot.accept();
  import.meta.hot.dispose((data) => {
    data.selectedDocument = selected;
  });
}
