import { Activity, Database, Gauge, HeartPulse, ListFilter, Server } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, Metric, PanelTitle } from "../components/common";
import { compactJSON, formatValue, percentValue } from "../lib/format";
import { renderHealthCell } from "../lib/logs";

export function OverviewTab() {
  const { health, healthyCount, modelStats, quotaRows, stopReasons, toolRate, usage } = useAdmin();
  return (
    <div className="view-grid">
      <section className="metric-grid">
        <Metric icon={HeartPulse} label="Healthy nodes" value={`${healthyCount}/${health.length}`} />
        <Metric icon={Gauge} label="Active requests" value={formatValue(usage.active_requests ?? usage.active ?? 0)} />
        <Metric icon={Database} label="Quota scopes" value={quotaRows.length.toString()} />
        <Metric icon={ListFilter} label="Tool call rate" value={percentValue(toolRate.tool_call_rate)} />
      </section>

      <section className="panel wide">
        <PanelTitle icon={HeartPulse} title="Backend Health" />
        <DataTable
          columns={["name", "healthy", "score", "successes", "failures", "last_error"]}
          rows={health}
          empty="No backend health data"
          renderCell={(row, column) => renderHealthCell(row, column)}
        />
      </section>

      <section className="panel">
        <PanelTitle icon={Gauge} title="Quota" />
        <DataTable columns={["scope", "used"]} rows={quotaRows} empty="No quota usage" renderCell={(row, column) => formatValue(row[column])} />
      </section>

      <section className="panel">
        <PanelTitle icon={Activity} title="Stop Reasons" />
        <DataTable columns={["reason", "count", "percentage"]} rows={stopReasons} empty="No stop reason data" renderCell={(row, column) => column === "percentage" ? percentValue(row[column]) : formatValue(row[column])} />
      </section>

      <section className="panel wide">
        <PanelTitle icon={Server} title="Model Traffic" />
        <DataTable
          columns={["model_name", "total_requests", "stop_reasons"]}
          rows={modelStats}
          empty="No model traffic data"
          renderCell={(row, column) => column === "stop_reasons" ? compactJSON(row[column]) : formatValue(row[column])}
        />
      </section>
    </div>
  );
}
