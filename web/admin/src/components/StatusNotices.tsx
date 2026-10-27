import { cacheStatusLabel, cacheStatusMessage, cacheStatusTone, catalogStatusLabel, catalogStatusMessage, catalogStatusTone } from "../lib/observability";
import { traceStatusLabel, traceStatusMessage, traceStatusTone } from "../lib/traces";
import type { CacheStats, CatalogSnapshot, CatalogWarning, TraceDetailPayload } from "../types";
import { Badge } from "./ui/badge";

export function CacheStatus({ error, status, stats, loading }: { error: string; status: number | null; stats: CacheStats; loading: boolean }) {
  const message = cacheStatusMessage(error, status, stats, loading);
  if (!message) return null;
  return (
    <div className="inline-status">
      <Badge tone={cacheStatusTone(error, status, stats, loading)}>{cacheStatusLabel(error, status, stats, loading)}</Badge>
      <span>{message}</span>
    </div>
  );
}

export function CatalogStatus({ error, status, snapshot, loading }: { error: string; status: number | null; snapshot: CatalogSnapshot | null; loading: boolean }) {
  const message = catalogStatusMessage(error, status, snapshot, loading);
  if (!message) return null;
  return (
    <div className="inline-status catalog-status">
      <Badge tone={catalogStatusTone(error, status, snapshot, loading)}>{catalogStatusLabel(error, status, snapshot, loading)}</Badge>
      <span>{message}</span>
    </div>
  );
}

export function CatalogWarnings({ warnings }: { warnings: CatalogWarning[] }) {
  if (warnings.length === 0) {
    return (
      <div className="validation-summary">
        <Badge tone="good">No catalog warnings</Badge>
      </div>
    );
  }
  return (
    <div className="validation-list">
      <div className="validation-summary">
        <Badge tone="warn">{warnings.length} warnings</Badge>
      </div>
      {warnings.map((warning, index) => (
        <div className="validation-row catalog-hint-row" key={`${warning.code || "warning"}-${warning.model || ""}-${warning.node || ""}-${index}`}>
          <Badge tone={warning.severity === "warning" ? "warn" : "neutral"}>{warning.severity || "info"}</Badge>
          <code>{warning.code || "catalog_warning"}</code>
          <span>{warning.message || "Catalog advisory warning"}</span>
        </div>
      ))}
    </div>
  );
}

export function TraceStatus({ error, status, detail, loading }: { error: string; status: number | null; detail: TraceDetailPayload | null; loading: boolean }) {
  const message = traceStatusMessage(error, status, detail, loading);
  if (!message) return null;
  return (
    <div className="inline-status trace-status">
      <Badge tone={traceStatusTone(error, status, detail, loading)}>{traceStatusLabel(error, status, detail, loading)}</Badge>
      <span>{message}</span>
    </div>
  );
}
