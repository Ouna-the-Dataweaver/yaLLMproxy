import { Activity, BarChart3, Database, FileJson, GitBranch, KeyRound, ListFilter, Network, Server } from "lucide-react";
import type { TabID } from "../types";

export type TabGroup = "Monitor" | "Configure";

export const tabGroups: TabGroup[] = ["Monitor", "Configure"];

export const tabs: Array<{ id: TabID; label: string; icon: typeof Activity; group: TabGroup }> = [
  { id: "overview", label: "Overview", icon: Activity, group: "Monitor" },
  { id: "observability", label: "Observability", icon: BarChart3, group: "Monitor" },
  { id: "logs", label: "Logs", icon: Database, group: "Monitor" },
  { id: "traces", label: "Traces", icon: GitBranch, group: "Monitor" },
  { id: "catalog", label: "Catalog", icon: ListFilter, group: "Monitor" },
  { id: "routing", label: "Models & Nodes", icon: Network, group: "Configure" },
  { id: "keys", label: "App Keys", icon: KeyRound, group: "Configure" },
  { id: "config", label: "Config", icon: FileJson, group: "Configure" },
  { id: "models", label: "Legacy Models", icon: Server, group: "Configure" }
];

export function tabFromLocation(location: Location): TabID {
  const tabParam = new URLSearchParams(location.search).get("tab");
  if (tabParam === "observability" || tabParam === "traces" || tabParam === "catalog" || tabParam === "logs" || tabParam === "config" || tabParam === "models" || tabParam === "routing" || tabParam === "keys") return tabParam;
  if (traceIDFromLocation(location)) return "traces";
  const segment = location.pathname.replace(/\/+$/, "").split("/").pop();
  if (segment === "observability" || segment === "traces" || segment === "catalog" || segment === "logs" || segment === "config" || segment === "models" || segment === "routing" || segment === "keys") return segment;
  return "overview";
}

export function pathForTab(tab: TabID) {
  if (tab === "overview") return "/admin";
  if (tab === "models") return "/admin/models";
  return `/admin?tab=${tab}`;
}

export function pathForTrace(traceID: string) {
  return `/admin?tab=traces&trace_id=${encodeURIComponent(traceID)}`;
}

export function traceIDFromLocation(location: Location) {
  const params = new URLSearchParams(location.search);
  const fromQuery = (params.get("trace_id") || params.get("trace") || "").trim();
  if (fromQuery) return fromQuery;
  const match = location.pathname.match(/\/admin\/traces\/([^/?#]+)/);
  return match ? decodeURIComponent(match[1]).trim() : "";
}

export function statusLine(backends: number, healthy: number, totalLogs: number) {
  return `${healthy}/${backends} backends healthy | ${totalLogs} log rows`;
}
