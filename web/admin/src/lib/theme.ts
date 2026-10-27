import { useEffect, useState } from "react";

export type ThemePreference = "system" | "light" | "dark";

const themeStorageKey = "yallmp.theme";
const order: ThemePreference[] = ["system", "light", "dark"];

export function readThemePreference(): ThemePreference {
  try {
    const stored = localStorage.getItem(themeStorageKey);
    return stored === "light" || stored === "dark" ? stored : "system";
  } catch {
    return "system";
  }
}

// applyThemePreference sets data-theme for explicit choices; "system" leaves it
// unset so the prefers-color-scheme media query in styles.css decides.
export function applyThemePreference(preference: ThemePreference) {
  const root = document.documentElement;
  if (preference === "system") {
    delete root.dataset.theme;
  } else {
    root.dataset.theme = preference;
  }
}

export function useThemePreference() {
  const [preference, setPreference] = useState<ThemePreference>(readThemePreference);
  useEffect(() => {
    applyThemePreference(preference);
    try {
      if (preference === "system") localStorage.removeItem(themeStorageKey);
      else localStorage.setItem(themeStorageKey, preference);
    } catch {
      // Storage may be unavailable; the choice still applies to this page.
    }
  }, [preference]);
  const cycle = () => setPreference((current) => order[(order.indexOf(current) + 1) % order.length]);
  return { preference, cycle };
}
