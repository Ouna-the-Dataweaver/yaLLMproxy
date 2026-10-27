import { Activity, BarChart3, Gauge, HardDrive, HeartPulse, RefreshCw } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, Metric, PanelTitle, StatList } from "../components/common";
import { CacheStatus } from "../components/StatusNotices";
import { Badge } from "../components/ui/badge";
import { formatValue, percentValue } from "../lib/format";
import { renderHealthCell } from "../lib/logs";
import { cacheEnabledLabel, cacheEnabledTone, cacheEntriesValue, cacheHitRate, cacheModeLabel, cacheModeTone, cacheTableEmpty, renderCacheValue } from "../lib/observability";
import { renderTraceID } from "../lib/traces";

export function ObservabilityTab() {
  const { cacheError, cacheRows, cacheStats, cacheStatus, health, healthyCount, observabilityLoading, observabilityTraceID, quotaRows, usage } = useAdmin();
  const exactCacheRows = cacheRows.filter((row) => !String(row.id).startsWith("semantic_"));
  const semanticCacheRows = cacheRows
    .filter((row) => String(row.id).startsWith("semantic_"))
    .map((row) => ({ ...row, field: String(row.field).replace(/^semantic /, "") }));
  return (
    <div className="view-grid observability-view">
      <section className="metric-grid">
        <Metric icon={Gauge} label="Cache hit rate" value={percentValue(cacheHitRate(cacheStats))} />
        <Metric icon={HardDrive} label="Cache entries" value={cacheEntriesValue(cacheStats)} />
        <Metric icon={RefreshCw} label="Cache evictions" value={formatValue(cacheStats.evictions ?? 0)} />
        <Metric icon={Activity} label="Active requests" value={formatValue(usage.active_requests ?? usage.active ?? 0)} />
      </section>

      <section className="panel wide">
        <div className="panel-header">
          <PanelTitle icon={HardDrive} title="Inference Cache" />
          <div className="toolbar">
            {observabilityLoading && <Badge tone="warn">loading</Badge>}
            <Badge tone={cacheEnabledTone(cacheStats)}>{cacheEnabledLabel(cacheStats)}</Badge>
            <Badge tone={cacheModeTone(cacheStats.mode)}>{cacheModeLabel(cacheStats.mode)}</Badge>
          </div>
        </div>
        <CacheStatus error={cacheError} status={cacheStatus} stats={cacheStats} loading={observabilityLoading} />
        <StatList rows={exactCacheRows} render={renderCacheValue} empty={cacheTableEmpty(cacheError, cacheStatus, observabilityLoading)} />
        {semanticCacheRows.length > 0 && (
          <>
            <h4 className="subheading">Semantic cache</h4>
            <StatList rows={semanticCacheRows} render={renderCacheValue} empty="" />
          </>
        )}
      </section>

      <section className="panel">
        <div className="panel-header">
          <PanelTitle icon={HeartPulse} title="Backend Health" />
          <Badge tone={healthyCount === health.length ? "good" : "warn"}>{healthyCount}/{health.length} up</Badge>
        </div>
        <DataTable
          columns={["name", "healthy", "score", "successes", "failures", "last_error"]}
          rows={health}
          empty="No backend health data"
          renderCell={(row, column) => renderHealthCell(row, column)}
        />
      </section>

      <section className="panel">
        <div className="panel-header">
          <PanelTitle icon={Gauge} title="Usage And Quota" />
          <Badge>{quotaRows.length} scopes</Badge>
        </div>
        <DataTable columns={["scope", "used"]} rows={quotaRows} empty="No quota usage" renderCell={(row, column) => formatValue(row[column])} />
      </section>

      {observabilityTraceID && (
        <section className="panel wide">
          <div className="panel-header">
            <PanelTitle icon={BarChart3} title="Trace" />
            {renderTraceID(observabilityTraceID)}
          </div>
        </section>
      )}
    </div>
  );
}
