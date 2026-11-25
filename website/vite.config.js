import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { catalog, contentFor, releaseMetadata } from "./content.mjs";

const repository = fileURLToPath(new URL("../", import.meta.url));
const docsModule = "\0virtual:agentflow-docs";

const releaseModule = "\0virtual:agentflow-release";

function docs() {
  const sources = catalog.map(([, , , path]) => fileURLToPath(new URL(path, new URL("../", import.meta.url))));
  return {
    name: "agentflow-docs",
    resolveId(id) {
      if (id === "virtual:agentflow-docs") return docsModule;
      if (id === "virtual:agentflow-release") return releaseModule;
    },
    load(id) {
      if (id === releaseModule) {
        const changelog = fileURLToPath(new URL("../CHANGELOG.md", import.meta.url));
        this.addWatchFile(changelog);
        return `export const site = ${JSON.stringify(releaseMetadata(process.env, readFileSync(changelog, "utf8")))};`;
      }
      if (id !== docsModule) return;
      for (const source of sources) this.addWatchFile(source);
      const content = contentFor(repository, process.env);
      return `export const documents = ${JSON.stringify(content.documents)};`;
    },
    handleHotUpdate({ file, server }) {
      if (!sources.includes(file)) return;
      const module = server.moduleGraph.getModuleById(docsModule);
      if (module) server.moduleGraph.invalidateModule(module);
      const release = server.moduleGraph.getModuleById(releaseModule);
      if (release) server.moduleGraph.invalidateModule(release);
      server.ws.send({ type: "full-reload" });
      return [];
    },
  };
}

export default defineConfig({
  root: "src",
  plugins: [react(), docs()],
  server: {
    port: 5173,
    strictPort: true,
    watch: process.env.VITE_USE_POLLING === "1" ? { usePolling: true, interval: 250 } : undefined,
  },
  build: {
    outDir: "../dist",
    emptyOutDir: true,
  },
});
