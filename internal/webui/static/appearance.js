// Set the theme before the first paint, including when the checkout is still connecting.
(() => {
  const key = "devflow.appearance";
  const system = window.matchMedia("(prefers-color-scheme: dark)");
  const preference = (value) => value === "light" || value === "dark" ? value : "system";
  let mode = "system";
  try { mode = preference(localStorage.getItem(key)); } catch { /* Browser storage is optional. */ }

  function apply() {
    document.documentElement.dataset.theme = mode === "system"
      ? (system.matches ? "dark" : "light") : mode;
    const select = document.getElementById("appearance");
    if (select) select.value = mode;
  }

  apply();
  system.addEventListener("change", apply);
  window.addEventListener("storage", (event) => {
    if (event.key === key || event.key === null) {
      mode = preference(event.newValue);
      apply();
    }
  });
  document.addEventListener("DOMContentLoaded", () => {
    const select = document.getElementById("appearance");
    if (!select) return;
    select.value = mode;
    select.addEventListener("change", () => {
      mode = preference(select.value);
      apply();
      try { localStorage.setItem(key, mode); } catch { /* The choice still applies to this tab. */ }
    });
  });
})();
