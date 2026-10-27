import { Activity, Clock3, Copy, Gauge, GitBranch, Network, RefreshCw, Search } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, Metric, PanelTitle } from "../components/common";
import { TraceStatus } from "../components/StatusNotices";
import { TraceSummaryValue, TraceTimeline } from "../components/TraceTimeline";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { asArray, copyText, formatDuration, formatTime, formatValue } from "../lib/format";
import { renderStringBadges } from "../lib/routing";
import { renderTraceAttemptCell, renderTraceErrorCell, renderTraceID, renderTraceOutcomeBadges } from "../lib/traces";
import type { JsonRecord, TraceAttemptRecord, TraceErrorRecord } from "../types";

export function TracesTab() {
  const { inspectTraceRequest, loadTraceDashboard, setTraceInput, submitTraceSearch, traceAttempts, traceDetail, traceError, traceErrors, traceInput, traceLoading, traceRequests, traceStatus, traceTimeline } = useAdmin();
  return (
    <div className="traces-view">
      <section className="panel wide">
        <div className="panel-header">
          <PanelTitle icon={GitBranch} title="Trace Lookup" />
          <div className="toolbar">
            {traceLoading && <Badge tone="warn">loading</Badge>}
            {traceDetail?.trace_id && renderTraceID(traceDetail.trace_id)}
            {traceDetail?.trace_id && (
              <Button variant="ghost" size="icon" onClick={() => void copyText(traceDetail.trace_id || "")} title="Copy trace ID" aria-label="Copy trace ID">
                <Copy size={16} aria-hidden="true" />
              </Button>
            )}
            <Button variant="secondary" size="icon" onClick={() => void loadTraceDashboard()} disabled={!traceInput.trim() || traceLoading} title="Reload trace">
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </div>
        <form className="trace-search-form" onSubmit={submitTraceSearch}>
          <label className="field trace-search-field">
            <span>Trace ID</span>
            <input value={traceInput} onChange={(event) => setTraceInput(event.target.value)} autoComplete="off" />
          </label>
          <Button size="icon" disabled={traceLoading || !traceInput.trim()} title="Load trace">
            <Search size={17} aria-hidden="true" />
          </Button>
        </form>
        <TraceStatus error={traceError} status={traceStatus} detail={traceDetail} loading={traceLoading} />
      </section>

      {traceDetail && (
        <>
          <section className="metric-grid trace-metrics">
            <Metric icon={GitBranch} label="Requests" value={formatValue(traceDetail.request_count ?? traceDetail.total ?? traceRequests.length)} />
            <Metric icon={Network} label="Attempts" value={formatValue(traceAttempts.length)} />
            <Metric icon={Activity} label="Errors" value={formatValue(traceErrors.length)} />
            <Metric icon={Clock3} label="Total duration" value={formatDuration(traceDetail.duration_ms)} />
          </section>

          <section className="panel wide">
            <div className="panel-header">
              <PanelTitle icon={Gauge} title="Trace Summary" />
              <div className="toolbar">
                <Badge tone={traceDetail.has_more ? "warn" : "neutral"}>{traceDetail.has_more ? "partial" : "complete"}</Badge>
                <Badge>{traceDetail.returned ?? traceRequests.length} returned</Badge>
              </div>
            </div>
            <div className="trace-summary-grid">
              <TraceSummaryValue label="Started" value={formatTime(traceDetail.started_at)} />
              <TraceSummaryValue label="Ended" value={formatTime(traceDetail.ended_at)} />
              <TraceSummaryValue label="Models" value={renderStringBadges(asArray<string>(traceDetail.models))} />
              <TraceSummaryValue label="Backends" value={renderStringBadges(asArray<string>(traceDetail.backends))} />
              <TraceSummaryValue label="Outcomes" value={renderTraceOutcomeBadges(asArray<string>(traceDetail.outcomes))} />
            </div>
          </section>

          <section className="panel wide">
            <div className="panel-header">
              <PanelTitle icon={Clock3} title="Timeline" />
              <Badge>{traceTimeline.length} events</Badge>
            </div>
            <TraceTimeline items={traceTimeline} onOpenLog={inspectTraceRequest} />
          </section>

          <section className="trace-grid">
            <div className="panel">
              <div className="panel-header">
                <PanelTitle icon={Network} title="Attempts" />
                <Badge>{traceAttempts.length} rows</Badge>
              </div>
              <DataTable
                columns={["request_id", "attempt_index", "retry_index", "backend_name", "status", "outcome", "retryable", "duration_ms", "error_type"]}
                rows={traceAttempts as JsonRecord[]}
                empty="No attempts returned"
                renderCell={(row, column) => renderTraceAttemptCell(row as TraceAttemptRecord, column)}
              />
            </div>

            <div className="panel">
              <div className="panel-header">
                <PanelTitle icon={Activity} title="Linked Errors" />
                <Badge>{traceErrors.length} rows</Badge>
              </div>
              <DataTable
                columns={["timestamp", "request_log_id", "error_type", "model_name", "backend_name", "http_status"]}
                rows={traceErrors as JsonRecord[]}
                empty="No linked errors"
                renderCell={(row, column) => renderTraceErrorCell(row as TraceErrorRecord, column)}
              />
            </div>
          </section>
        </>
      )}
    </div>
  );
}
