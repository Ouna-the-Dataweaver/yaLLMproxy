import { Badge } from "../components/ui/badge";
import type { BadgeTone, CacheStats, CatalogModelEntry, CatalogNodeEntry, CatalogProviderEntry, CatalogRow, CatalogSnapshot, JsonRecord } from "../types";
import { APIError } from "./api";
import { asArray, formatBytes, formatTime, formatValue, numberValue } from "./format";

export function hasCacheSnapshot(stats: CacheStats) {
  return Object.keys(stats).length > 0;
}

export function cacheHitRate(stats: CacheStats) {
  const hits = numberValue(stats.hits);
  const misses = numberValue(stats.misses);
  const total = hits + misses;
  return total > 0 ? hits / total : 0;
}

export function cacheEntriesValue(stats: CacheStats) {
  if (!hasCacheSnapshot(stats)) return "-";
  const entries = formatValue(stats.entries ?? 0);
  return typeof stats.max_entries === "number" && stats.max_entries > 0 ? `${entries}/${stats.max_entries}` : entries;
}

export function cacheSnapshotRows(stats: CacheStats): JsonRecord[] {
  if (!hasCacheSnapshot(stats)) return [];
  const rows: JsonRecord[] = [
    { id: "mode", field: "mode", value: cacheModeLabel(stats.mode) },
    { id: "enabled", field: "enabled", value: typeof stats.enabled === "boolean" ? (stats.enabled ? "yes" : "no") : "unknown" },
    { id: "hits", field: "hits", value: stats.hits ?? 0 },
    { id: "misses", field: "misses", value: stats.misses ?? 0 },
    { id: "writes", field: "writes", value: stats.writes ?? 0 },
    { id: "evictions", field: "evictions", value: stats.evictions ?? 0 },
    { id: "entries", field: "entries", value: stats.entries ?? 0 },
    { id: "total_entry_bytes", field: "total entry bytes", value: stats.total_entry_bytes, format: "bytes" },
    { id: "max_entries", field: "max entries", value: stats.max_entries },
    { id: "max_entry_bytes", field: "max entry bytes", value: stats.max_entry_bytes, format: "bytes" }
  ];
  if (stats.semantic) {
    rows.push(
      { id: "semantic_enabled", field: "semantic enabled", value: typeof stats.semantic.enabled === "boolean" ? (stats.semantic.enabled ? "yes" : "no") : "unknown" },
      { id: "semantic_mode", field: "semantic mode", value: cacheModeLabel(stats.semantic.mode) },
      { id: "semantic_hits", field: "semantic hits", value: stats.semantic.hits ?? 0 },
      { id: "semantic_misses", field: "semantic misses", value: stats.semantic.misses ?? 0 },
      { id: "semantic_writes", field: "semantic writes", value: stats.semantic.writes ?? 0 },
      { id: "semantic_evictions", field: "semantic evictions", value: stats.semantic.evictions ?? 0 },
      { id: "semantic_embedding_errors", field: "semantic embedding errors", value: stats.semantic.embedding_errors ?? 0 },
      { id: "semantic_threshold_rejected", field: "semantic threshold rejected", value: stats.semantic.threshold_rejected ?? 0 },
      { id: "semantic_entries", field: "semantic entries", value: stats.semantic.entries ?? 0 },
      { id: "semantic_total_entry_bytes", field: "semantic total entry bytes", value: stats.semantic.total_entry_bytes, format: "bytes" },
      { id: "semantic_max_entries", field: "semantic max entries", value: stats.semantic.max_entries },
      { id: "semantic_max_entry_bytes", field: "semantic max entry bytes", value: stats.semantic.max_entry_bytes, format: "bytes" },
      { id: "semantic_top_k", field: "semantic top k", value: stats.semantic.top_k },
      { id: "semantic_similarity_threshold", field: "semantic similarity threshold", value: stats.semantic.similarity_threshold }
    );
  }
  return rows;
}

export function renderCacheValue(row: JsonRecord) {
  return row.format === "bytes" ? formatBytes(row.value) : formatValue(row.value);
}

export function catalogSnapshotRows(snapshot: CatalogSnapshot | null): CatalogRow[] {
  if (!snapshot?.enabled) return [];
  const rows: CatalogRow[] = [];
  for (const model of asArray<CatalogModelEntry>(snapshot.models)) {
    const modelName = model.model || "-";
    for (const provider of asArray<CatalogProviderEntry>(model.providers)) {
      const providerName = provider.provider || "-";
      for (const node of asArray<CatalogNodeEntry>(provider.nodes)) {
        rows.push({
          model: modelName,
          provider: providerName,
          node: node.node || "-",
          status: node.status || "unknown",
          source: node.source || "-",
          error_class: node.error_class || "",
          configured_model: node.configured_model || "",
          target_model: node.target_model || "",
          last_sync: node.last_sync,
          next_sync: node.next_sync
        });
      }
    }
  }
  return rows;
}

export function renderCatalogCell(row: CatalogRow, column: string) {
  if (column === "model") return <span className="resource-name">{row.model}</span>;
  if (column === "provider" || column === "node") return formatValue(row[column]);
  if (column === "status") return <Badge tone={catalogNodeStatusTone(row.status, row.source)}>{row.status}</Badge>;
  if (column === "source") return <Badge tone={row.source === "probe" ? "good" : "neutral"}>{row.source || "-"}</Badge>;
  if (column === "error_class") return row.error_class ? <Badge tone={catalogErrorClassTone(row.error_class)}>{row.error_class}</Badge> : "-";
  if (column === "last_sync" || column === "next_sync") return formatTime(row[column]);
  return formatValue(row[column]);
}

export function catalogNodeStatusTone(status: string, source: string): BadgeTone {
  if (status === "available") return "good";
  if (status === "stale") return "warn";
  if (status === "unavailable") return "bad";
  if (status === "configured" && source === "probe") return "warn";
  return "neutral";
}

export function catalogErrorClassTone(errorClass: string): BadgeTone {
  if (errorClass === "auth" || errorClass === "status_error" || errorClass === "invalid_response" || errorClass === "unsupported_api_type") return "bad";
  if (errorClass === "rate_limited" || errorClass === "timeout" || errorClass === "network") return "warn";
  return "neutral";
}

export function catalogSafeErrorMessage(err: unknown) {
  if (err instanceof APIError) {
    if (err.status === 401 || err.status === 403) return "Admin password is required or invalid for /admin/catalog.";
    if (err.status === 404) return "This backend does not expose /admin/catalog.";
    return "Catalog snapshot is unavailable.";
  }
  return "Catalog snapshot is unavailable.";
}

export function catalogStatusMessage(error: string, status: number | null, snapshot: CatalogSnapshot | null, loading: boolean) {
  if (loading && !snapshot) return "Loading catalog snapshot.";
  if (error) return error;
  if (!snapshot) return "No catalog snapshot loaded.";
  if (snapshot.enabled === false) return "Model catalog is disabled; configured routes and probes are not shown here.";
  if (snapshot.enabled === true && snapshot.probe_enabled === false) return "Catalog is enabled, but no nodes or providers are opted into probes.";
  return "";
}

export function catalogStatusLabel(error: string, status: number | null, snapshot: CatalogSnapshot | null, loading: boolean) {
  if (loading && !snapshot) return "loading";
  if (error) {
    if (status === 401 || status === 403) return "auth";
    if (status === 404) return "missing";
    return "error";
  }
  if (!snapshot) return "empty";
  if (snapshot.enabled === false) return "disabled";
  if (snapshot.probe_enabled === false) return "probe off";
  return "ready";
}

export function catalogStatusTone(error: string, status: number | null, snapshot: CatalogSnapshot | null, loading: boolean): BadgeTone {
  if (loading && !snapshot) return "warn";
  if (error) return status === 404 ? "warn" : "bad";
  if (!snapshot) return "neutral";
  if (snapshot.enabled === false) return "neutral";
  if (snapshot.probe_enabled === false) return "warn";
  return "good";
}

export function catalogTableEmpty(snapshot: CatalogSnapshot | null, error: string, status: number | null, loading: boolean) {
  if (loading) return "Loading catalog";
  if (error && (status === 401 || status === 403)) return "Admin password required for catalog";
  if (error && status === 404) return "Catalog endpoint not available";
  if (error) return "Catalog unavailable";
  if (snapshot?.enabled === false) return "Catalog disabled";
  return "No catalog models";
}

export function normalizedCacheMode(mode?: string) {
  return (mode || "").trim().toLowerCase();
}

export function cacheModeLabel(mode?: string) {
  const normalized = normalizedCacheMode(mode);
  return normalized || "unknown";
}

export function cacheModeTone(mode?: string): BadgeTone {
  const normalized = normalizedCacheMode(mode);
  if (normalized === "read_write") return "good";
  if (normalized === "read_only" || normalized === "write_only") return "warn";
  return "neutral";
}

export function cacheEnabledLabel(stats: CacheStats) {
  if (!hasCacheSnapshot(stats)) return "unknown";
  if (stats.enabled === true) return "enabled";
  if (stats.enabled === false) return "disabled";
  return "unknown";
}

export function cacheEnabledTone(stats: CacheStats): BadgeTone {
  if (!hasCacheSnapshot(stats)) return "neutral";
  return stats.enabled === true ? "good" : "neutral";
}

export function cacheStatusMessage(error: string, status: number | null, stats: CacheStats, loading: boolean) {
  if (loading && !hasCacheSnapshot(stats)) return "Loading cache counters.";
  if (error) {
    if (status === 401 || status === 403) return "Admin password is required or invalid for /admin/cache.";
    if (status === 404) return "This backend does not expose /admin/cache.";
    return error;
  }
  if (!hasCacheSnapshot(stats)) return "No cache snapshot loaded.";
  const mode = normalizedCacheMode(stats.mode);
  if (stats.enabled === false || mode === "off" || mode === "disabled") {
    return "Exact inference cache is disabled; no prompt or response content is exposed here.";
  }
  return "";
}

export function cacheStatusLabel(error: string, status: number | null, stats: CacheStats, loading: boolean) {
  if (loading && !hasCacheSnapshot(stats)) return "loading";
  if (error) {
    if (status === 401 || status === 403) return "auth";
    if (status === 404) return "missing";
    return "error";
  }
  if (!hasCacheSnapshot(stats)) return "empty";
  return "disabled";
}

export function cacheStatusTone(error: string, status: number | null, stats: CacheStats, loading: boolean): BadgeTone {
  if (loading && !hasCacheSnapshot(stats)) return "warn";
  if (error) return status === 404 ? "warn" : "bad";
  if (!hasCacheSnapshot(stats)) return "neutral";
  return "neutral";
}

export function cacheTableEmpty(error: string, status: number | null, loading: boolean) {
  if (loading) return "Loading cache counters";
  if (error && (status === 401 || status === 403)) return "Admin password required for cache counters";
  if (error && status === 404) return "Cache endpoint not available";
  if (error) return "Cache counters unavailable";
  return "No cache snapshot";
}
