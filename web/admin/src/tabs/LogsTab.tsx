import { ChevronLeft, ChevronRight, Database, Eye, ListFilter, RefreshCw } from "lucide-react";
import { useAdmin } from "../admin/context";
import { PanelTitle } from "../components/common";
import { LogInspector } from "../components/LogInspector";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { formatDuration, formatTime, formatValue, isNonEmptyString } from "../lib/format";
import { formatLogRange, outcomeTone, stopReasonTone, tokenDetailTitle } from "../lib/logs";
import { renderTraceID, traceIDFromRecord } from "../lib/traces";

export function LogsTab() {
  const { clearLogFilters, filters, loadLogs, logMeta, logStopReasonOptions, logs, logsError, logsHaveTraceIDs, logsLoading, models, openTraceDashboard, selectAdjacentLog, selectLog, selectedLog, selectedLogError, selectedLogID, selectedLogIndex, selectedLogLoading, setFilters, submitLogFilters } = useAdmin();
  return (
    <div className="logs-view">
      <section className="panel logs-list-panel">
        <div className="panel-header">
          <PanelTitle icon={Database} title="Request Logs" />
          <div className="toolbar">
            {logsLoading && <Badge tone="warn">loading</Badge>}
            <Badge>{formatLogRange(logMeta, logs.length)}</Badge>
            <Button variant="secondary" size="icon" onClick={() => void loadLogs(logMeta.offset)} disabled={logsLoading} title="Reload logs" aria-label="Reload logs">
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </div>
        <form className="log-filter-form" onSubmit={submitLogFilters}>
          <label className="field">
            <span>Model</span>
            <input placeholder="Any model" value={filters.model} onChange={(event) => setFilters({ ...filters, model: event.target.value })} list="log-model-options" autoComplete="off" />
            <datalist id="log-model-options">
              {models.map((model) => model.id).filter(isNonEmptyString).map((name) => <option key={name} value={name} />)}
            </datalist>
          </label>
          <label className="field">
            <span>Outcome</span>
            <select value={filters.outcome} onChange={(event) => setFilters({ ...filters, outcome: event.target.value })}>
              <option value="">Any outcome</option>
              <option value="success">Success</option>
              <option value="error">Error</option>
              <option value="cancelled">Cancelled</option>
            </select>
          </label>
          <label className="field">
            <span>Stop reason</span>
            <select value={filters.stopReason} onChange={(event) => setFilters({ ...filters, stopReason: event.target.value })}>
              <option value="">Any reason</option>
              {logStopReasonOptions.map((reason) => <option key={reason} value={reason}>{reason}</option>)}
            </select>
          </label>
          <label className="field">
            <span>Tool calls</span>
            <select value={filters.toolCall} onChange={(event) => setFilters({ ...filters, toolCall: event.target.value })}>
              <option value="">Either</option>
              <option value="true">Only tool calls</option>
              <option value="false">No tool calls</option>
            </select>
          </label>
          <label className="field">
            <span>From</span>
            <input type="datetime-local" value={filters.startDate} onChange={(event) => setFilters({ ...filters, startDate: event.target.value })} />
          </label>
          <label className="field">
            <span>To</span>
            <input type="datetime-local" value={filters.endDate} onChange={(event) => setFilters({ ...filters, endDate: event.target.value })} />
          </label>
          <label className="field log-filter-search">
            <span>Request content</span>
            <input type="search" placeholder="Search stored body" value={filters.search} onChange={(event) => setFilters({ ...filters, search: event.target.value })} />
          </label>
          <div className="log-filter-actions">
            <Button type="button" variant="ghost" size="sm" onClick={clearLogFilters} disabled={logsLoading}>Clear</Button>
            <Button type="submit" size="sm" disabled={logsLoading}>
              <ListFilter size={16} aria-hidden="true" />
              Apply
            </Button>
          </div>
        </form>
        {logsError && <div className="inline-status" role="alert"><Badge tone="bad">error</Badge><span>{logsError}</span></div>}
        <div className="table-wrap">
          <table className="logs-table">
            <caption className="sr-only">Filtered proxy request logs</caption>
            <thead>
              <tr>
                <th>Time</th>
                <th>Model</th>
                <th>Status</th>
                <th>Stop</th>
                <th className="num">Tokens</th>
                <th className="num">Duration</th>
                {logsHaveTraceIDs && <th>Trace</th>}
              </tr>
            </thead>
            <tbody>
              {logs.map((row, index) => (
                <tr key={row.id || index} className={row.id === selectedLogID ? "selected-row" : undefined}>
                  <td>
                    <button type="button" className="log-open-button" onClick={() => void selectLog(row.id)} aria-label={`Inspect request from ${formatTime(row.request_time || row.created_at)}`}>
                      {formatTime(row.request_time || row.created_at)}
                    </button>
                  </td>
                  <td><span className="resource-name">{row.model_name || "-"}</span>{row.is_tool_call && <Badge>tool</Badge>}</td>
                  <td>
                    <Badge tone={outcomeTone(row)}>{row.outcome || row.backend_status || "-"}</Badge>
                    {Number(row.warning_count || 0) > 0 && <Badge tone="warn">{row.warning_count} warn</Badge>}
                  </td>
                  <td>{row.stop_reason ? <Badge tone={stopReasonTone(row.stop_reason)}>{row.stop_reason}</Badge> : "-"}</td>
                  <td className="num" title={tokenDetailTitle(row)}>{formatValue(row.total_tokens ?? row.usage_stats?.total_tokens)}</td>
                  <td className="num">{formatDuration(row.duration_ms)}</td>
                  {logsHaveTraceIDs && <td>{renderTraceID(traceIDFromRecord(row), openTraceDashboard)}</td>}
                </tr>
              ))}
              {logs.length === 0 && (
                <tr>
                  <td colSpan={logsHaveTraceIDs ? 7 : 6} className="empty-cell">{logsLoading ? "Loading request logs" : "No logs match these filters"}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="pagination-bar" aria-label="Request log pages">
          <span>{formatLogRange(logMeta, logs.length)}</span>
          <div className="toolbar">
            <Button variant="secondary" size="sm" onClick={() => void loadLogs(Math.max(0, logMeta.offset - logMeta.limit))} disabled={logsLoading || logMeta.offset <= 0}>
              <ChevronLeft size={16} aria-hidden="true" /> Previous
            </Button>
            <Button variant="secondary" size="sm" onClick={() => void loadLogs(logMeta.offset + logMeta.limit)} disabled={logsLoading || !logMeta.hasMore}>
              Next <ChevronRight size={16} aria-hidden="true" />
            </Button>
          </div>
        </div>
      </section>

      <section className="panel log-detail-panel">
        <div className="panel-header">
          <PanelTitle icon={Eye} title="Request Inspector" />
          <div className="toolbar">
            {selectedLogLoading && <Badge tone="warn">loading</Badge>}
            {selectedLog && traceIDFromRecord(selectedLog) && renderTraceID(traceIDFromRecord(selectedLog), openTraceDashboard)}
          </div>
        </div>
        {selectedLogError && <div className="inline-status" role="alert"><Badge tone="bad">error</Badge><span>{selectedLogError}</span></div>}
        <LogInspector
          log={selectedLog}
          loading={selectedLogLoading}
          canPrevious={selectedLogIndex > 0}
          canNext={selectedLogIndex >= 0 && selectedLogIndex < logs.length - 1}
          onPrevious={() => selectAdjacentLog(-1)}
          onNext={() => selectAdjacentLog(1)}
        />
      </section>
    </div>
  );
}
