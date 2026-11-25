import assert from "node:assert/strict";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { catalog, contentFor, releaseMetadata, renderDocument } from "./content.mjs";
import { installerCommand } from "./src/install.js";

const tag = "v1.2.3";
const source = "## [1.2.3](https://github.invalid/compare/v1.2.2...v1.2.3) - Example";
const published = {
  AGENTFLOW_SITE_REPOSITORY: "nscaledev/agentflow",
  AGENTFLOW_SITE_RELEASE: tag,
  AGENTFLOW_SITE_RELEASE_SHA: "a".repeat(40),
};

test("release links follow heading versions, not the previous tag in a comparison URL", () => {
  const site = releaseMetadata(published, source);
  assert.deepEqual(site.tags, [tag]);
  assert.equal(site.tag, tag);
  assert.equal(releaseMetadata({}, source).tag, "");
  assert.equal(releaseMetadata({}, source).repository, "https://github.com/nscaledev/agentflow");
  assert.throws(() => releaseMetadata({ ...published, AGENTFLOW_SITE_REPOSITORY: "nscaledev/devflow" }, source), /canonical/);
  assert.throws(() => releaseMetadata({ ...published, AGENTFLOW_SITE_RELEASE: "v1.2.2" }, source), /changelog/);
});

test("relative links open the known documentation route and never render raw HTML", () => {
  const site = releaseMetadata({}, source);
  const doc = renderDocument("# Test\n[Providers](../providers.md#provider-definitions)\n<ScRiPt>alert(1)</ScRiPt>",
    { id: "enable", title: "Test", path: "docs/launch/enable.md", group: "Get started" }, site);
  assert.match(doc.html, /href="#docs\/providers\/provider-definitions"/);
  assert.ok(doc.html.includes("&lt;ScRiPt&gt;alert(1)&lt;/ScRiPt&gt;"));
});

test("every published document resolves at build time", () => {
  const root = fileURLToPath(new URL("../", import.meta.url));
  const { documents, site } = contentFor(root, {});
  assert.equal(documents.length, catalog.length);
  assert.ok(site.tags.includes("v0.52.2"));
  for (const doc of documents) {
    assert.ok(doc.html.length > 50, doc.path);
  }
});

test("both installer formats use the exact tag and verify provenance before execution", () => {
  for (const platform of ["windows", "posix"]) {
    const command = installerCommand(platform, tag);
    assert.match(command, /gh release download v1\.2\.3/);
    assert.match(command, /gh attestation verify/);
    assert.match(command, /--signer-workflow nscaledev\/agentflow\/\.github\/workflows\/release\.yml/);
    const execution = platform === "windows" ? "& $installer" : 'bash "$installer"';
    assert.ok(command.indexOf("gh attestation verify") < command.indexOf(execution));
  }
  assert.throws(() => installerCommand("posix", "latest"), /pinned release/);
});
