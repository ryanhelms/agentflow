import { useEffect, useState } from "react";
import { Button, Link, Logo, Row, Text } from "@nscaledev/design-components/atoms";
import "@nscaledev/design-tokens/tokens.css";
import "@nscaledev/design-components/styles.css";
import "@nscaledev/design-patterns/styles.css";
import "./style.css";
import Docs from "./Docs.jsx";
import Overview from "./Overview.jsx";

const themePreference = "devflow.website.theme";

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
  return location.hash === "#docs" ? "docs" : "overview";
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
    <div className="nscale-ui devflow-site" data-theme={activeTheme}>
      <header className="site-header">
        <Row className="site-header-inner" wrap justify="between" gap={4}>
          <Link href="#overview" className="site-brand" aria-label="Devflow overview">
            <Logo variant="full" height={14} />
            <Text className="site-product" weight="bold">Devflow</Text>
          </Link>
          <Row as="nav" aria-label="Devflow" className="site-nav" wrap gap={2}>
            <Link href="#overview" aria-current={page === "overview" ? "page" : undefined}>Overview</Link>
            <Link href="#docs" aria-current={page === "docs" ? "page" : undefined}>Documentation</Link>
            <Button variant="ghost" size="sm" icon={activeTheme === "dark" ? "sun" : "moon"}
              onClick={toggleTheme} aria-label={themeLabel} title={themeLabel} />
          </Row>
        </Row>
      </header>
      <main className="site-main">
        <Overview active={page === "overview"} />
        <Docs active={page === "docs"} />
      </main>
    </div>
  );
}
