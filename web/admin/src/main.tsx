import React from "react";
import ReactDOM from "react-dom/client";
import { AdminApp } from "./App";
import { applyThemePreference, readThemePreference } from "./lib/theme";
import "./styles.css";

applyThemePreference(readThemePreference());

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <AdminApp />
  </React.StrictMode>
);
