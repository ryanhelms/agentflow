import { lazy, Suspense, useEffect, useState } from "react";
import { Button, Link, Logo, Row, Text } from "@nscaledev/design-components/atoms";
import "@nscaledev/design-tokens/tokens.css";
import "@nscaledev/design-components/styles.css";
import "@nscaledev/design-patterns/styles.css";
import "./style.css";
import Overview from "./Overview.jsx";
import Demo from "./Demo.jsx";

const Docs = lazy(() => import("./Docs.jsx"));

const themePreference = "agentflow.website.theme";

function initialTheme() {
  try {
    const saved = localStorage.getItem(themePreference);
    if (saved === "light" || saved === "dark") return saved;
  } catch {
    // The site also works when browser storage is unavailable.
  }
  return null;
}

function currentPage() {
  const [section, id, anchor] = location.hash.slice(1).split("/");
  return { section: ["docs", "demo"].includes(section) ? section : "overview", id, anchor };
}

export default function App() {
  const [page, setPage] = useState(currentPage);
  const [theme, setTheme] = useState(initialTheme);
  const [darkSystem, setDarkSystem] = useState(() => matchMedia("(prefers-color-scheme: dark)").matches);
  const activeTheme = theme ?? (darkSystem ? "dark" : "light");

  useEffect(() => {
    function onNavigation() {
      setPage(currentPage());
      window.scrollTo(0, 0);
    }
    const colorScheme = matchMedia("(prefers-color-scheme: dark)");
    const onSchemeChange = (event) => setDarkSystem(event.matches);
    window.addEventListener("hashchange", onNavigation);
    colorScheme.addEventListener("change", onSchemeChange);
    return () => {
      window.removeEventListener("hashchange", onNavigation);
      colorScheme.removeEventListener("change", onSchemeChange);
    };
  }, []);

  function toggleTheme() {
    const next = activeTheme === "dark" ? "light" : "dark";
    setTheme(next);
    try {
      localStorage.setItem(themePreference, next);
    } catch {}
  }

  const themeLabel = activeTheme === "dark" ? "Use light theme" : "Use dark theme";
  return (
    <div className="nscale-ui agentflow-site" data-theme={activeTheme}>
      <header className="site-header">
        <Row className="site-header-inner" wrap justify="between" gap={4}>
          <Link href="#overview" className="site-brand" aria-label="Agentflow overview">
            <Logo variant="full" height={14} />
            <Text className="site-product" weight="bold">Agentflow</Text>
          </Link>
          <Row as="nav" aria-label="Agentflow" className="site-nav" wrap gap={2}>
            <Link href="#overview" aria-current={page.section === "overview" ? "page" : undefined}>Overview</Link>
            <Link href="#demo" aria-current={page.section === "demo" ? "page" : undefined}>Walkthrough</Link>
            <Link href="#docs" aria-current={page.section === "docs" ? "page" : undefined}>Documentation</Link>
            <Button variant="ghost" size="sm" icon={activeTheme === "dark" ? "sun" : "moon"}
              onClick={toggleTheme} aria-label={themeLabel} title={themeLabel} />
          </Row>
        </Row>
      </header>
      <main className="site-main">
        {page.section === "overview" && <Overview />}
        {page.section === "demo" && <Demo />}
        {page.section === "docs" && (
          <Suspense fallback={<p role="status">Loading documentation…</p>}>
            <Docs id={page.id} anchor={page.anchor} />
          </Suspense>
        )}
      </main>
    </div>
  );
}
