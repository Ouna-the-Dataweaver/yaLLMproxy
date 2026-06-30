import React from "react";
import ReactDOM from "react-dom/client";
import { AdminApp } from "./App";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <AdminApp />
  </React.StrictMode>
);
