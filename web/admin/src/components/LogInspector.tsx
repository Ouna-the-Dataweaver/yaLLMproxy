import { logTokenUsage } from "../logUsage";
import { Activity, ChevronLeft, ChevronRight, Copy, Download, Eye, FileJson, Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { copyText, detailValueLabel, downloadJSON, firstPresent, formatDuration, formatTime, formatValue, hasDetailValue, isJsonRecord, numberField, parseJSONText, prettyJSON, safeClassName, stringField } from "../lib/format";
import { conversationMessages, logDetailSections, omitFields, outcomeTone } from "../lib/logs";
import type { JsonRecord, LogDetailSection, LogRecord } from "../types";
import { PanelTitle } from "./common";
import { TraceSummaryValue } from "./TraceTimeline";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function LogInspector({
  log,
  loading,
  canPrevious,
  canNext,
  onPrevious,
  onNext
}: {
  log: JsonRecord | null;
  loading: boolean;
  canPrevious: boolean;
  canNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
}) {
  const [search, setSearch] = useState("");
  const [raw, setRaw] = useState(false);
  const [historyCollapsed, setHistoryCollapsed] = useState(false);
  const [expandedMessages, setExpandedMessages] = useState<Set<number>>(new Set());
  const inspectorRef = useRef<HTMLDivElement>(null);
  const logID = stringField(log, "id");
  const sections = useMemo(() => logDetailSections(log), [log]);
  const body = firstPresent(log?.request_body, log?.body);
  const bodyRecord = isJsonRecord(body) ? body : null;
  const messages = conversationMessages(bodyRecord);
  const normalizedSearch = search.trim().toLowerCase();
  const messagesMatch = messages.length > 0 && (!normalizedSearch || prettyJSON(messages).toLowerCase().includes(normalizedSearch));
  const filteredSections = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return sections;
    return sections.filter((section) => `${section.title}\n${prettyJSON(section.value)}`.toLowerCase().includes(query));
  }, [search, sections]);

  useEffect(() => {
    setSearch("");
    setRaw(false);
    setHistoryCollapsed(false);
    setExpandedMessages(new Set());
    if (inspectorRef.current) inspectorRef.current.scrollTop = 0;
  }, [logID]);

  if (!log) {
    return (
      <div className="log-detail-empty" aria-live="polite">
        <Eye size={26} aria-hidden="true" />
        <strong>{loading ? "Loading request" : "Select a request to inspect"}</strong>
        <span>Open a timestamp in the table to read prompts, messages, responses, retries, and warnings.</span>
      </div>
    );
  }

  const responseValue = firstPresent(log.response_body, parseJSONText(log.full_response));
  const filenameID = logID || "request";
  const historyView = historyCollapsed ? "collapsed" : expandedMessages.size === messages.length ? "expanded" : "headers";

  function changeHistoryView(view: string) {
    setHistoryCollapsed(view === "collapsed");
    setExpandedMessages(view === "expanded" ? new Set(messages.map((_, index) => index)) : new Set());
  }

  return (
    <div className="log-inspector" ref={inspectorRef}>
      <div className="log-detail-toolbar">
        <label className="detail-search-field">
          <Search size={15} aria-hidden="true" />
          <span className="sr-only">Search request detail</span>
          <input type="search" placeholder="Find in detail" value={search} onChange={(event) => setSearch(event.target.value)} />
        </label>
        <div className="toolbar log-detail-actions">
          <Button type="button" variant="ghost" size="sm" onClick={() => setRaw((value) => !value)} aria-pressed={raw}>
            <FileJson size={15} aria-hidden="true" /> {raw ? "Readable" : "Raw JSON"}
          </Button>
          <Button type="button" variant="ghost" size="icon" onClick={() => void copyText(prettyJSON(log))} title="Copy full log" aria-label="Copy full log">
            <Copy size={16} aria-hidden="true" />
          </Button>
          <Button type="button" variant="ghost" size="icon" onClick={() => downloadJSON(log, `yallmp-log-${filenameID}.json`)} title="Download full log" aria-label="Download full log">
            <Download size={16} aria-hidden="true" />
          </Button>
          <Button type="button" variant="secondary" size="icon" onClick={onPrevious} disabled={!canPrevious || loading} title="Previous request" aria-label="Previous request">
            <ChevronLeft size={16} aria-hidden="true" />
          </Button>
          <Button type="button" variant="secondary" size="icon" onClick={onNext} disabled={!canNext || loading} title="Next request" aria-label="Next request">
            <ChevronRight size={16} aria-hidden="true" />
          </Button>
        </div>
        {!raw && messagesMatch && (
          <div className="history-controls" role="group" aria-label="Conversation view">
            <span>History</span>
            {([["collapsed", "Collapsed"], ["headers", "Message headers"], ["expanded", "Expanded"]] as const).map(([view, label]) => (
              <Button key={view} type="button" variant={historyView === view ? "secondary" : "ghost"} size="sm" aria-pressed={historyView === view} onClick={() => changeHistoryView(view)}>{label}</Button>
            ))}
          </div>
        )}
      </div>

      <div className="log-overview-grid">
        <TraceSummaryValue label="Time" value={formatTime(stringField(log, "request_time") || stringField(log, "created_at"))} />
        <TraceSummaryValue label="Model" value={formatValue(log.model_name)} />
        <TraceSummaryValue label="Route" value={`${formatValue(log.method)} ${formatValue(log.path || log.request_path)}`} />
        <TraceSummaryValue label="Outcome" value={<Badge tone={outcomeTone(log as LogRecord)}>{formatValue(log.outcome || log.backend_status)}</Badge>} />
        <TraceSummaryValue label="Backend" value={formatValue(log.backend_name)} />
        <TraceSummaryValue label="Duration" value={formatDuration(numberField(log, "duration_ms"))} />
        <LogTokenSummary key={logID} log={log} />
        <TraceSummaryValue label="Stop reason" value={formatValue(log.stop_reason)} />
      </div>

      {typeof log.full_request_status === "string" && log.full_request_status !== "available" && (
        <div className="inline-status" role="status">
          <Badge tone="warn">payload {formatValue(log.full_request_status)}</Badge>
          <span>The metadata is available, but the stored request/response payload cannot be read.</span>
        </div>
      )}

      {raw ? (
        <pre className="json-block raw-log-block"><code>{prettyJSON(log)}</code></pre>
      ) : (
        <>
          {messagesMatch && (
            <section className="conversation-section">
              <div className="section-heading">
                <div><PanelTitle icon={Activity} title="Conversation" /><Badge>{messages.length} messages</Badge></div>
                <Button type="button" variant="ghost" size="icon" onClick={() => downloadJSON(body, `yallmp-request-${filenameID}.json`)} title="Download request body" aria-label="Download request body">
                  <Download size={16} aria-hidden="true" />
                </Button>
              </div>
              {!historyCollapsed && <MessageList messages={messages} expanded={expandedMessages} onToggle={(index) => setExpandedMessages((current) => {
                const next = new Set(current);
                if (next.has(index)) next.delete(index); else next.add(index);
                return next;
              })} />}
            </section>
          )}
          {filteredSections.map((section) => (
            <JsonDetailSection key={`${logID}-${section.id}`} section={section} />
          ))}
          {normalizedSearch && filteredSections.length === 0 && !messagesMatch && (
            <div className="log-detail-empty compact"><strong>No matching detail sections</strong><span>Try a broader search.</span></div>
          )}
          {(hasDetailValue(body) || hasDetailValue(responseValue)) && (
            <div className="payload-download-row">
              {hasDetailValue(body) && (
                <Button type="button" variant="secondary" size="sm" onClick={() => downloadJSON(body, `yallmp-request-${filenameID}.json`)}>
                  <Download size={15} aria-hidden="true" /> Download request
                </Button>
              )}
              {hasDetailValue(responseValue) && (
                <Button type="button" variant="secondary" size="sm" onClick={() => downloadJSON(responseValue, `yallmp-response-${filenameID}.json`)}>
                  <Download size={15} aria-hidden="true" /> Download response
                </Button>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function LogTokenSummary({ log }: { log: JsonRecord }) {
  const usage = logTokenUsage(log);
  const display = (value: number | undefined) => value === undefined ? "Not reported" : value.toLocaleString();
  return (
    <details className="trace-summary-value token-summary">
      <summary aria-label="Token breakdown">
        <span>Tokens <ChevronRight size={15} aria-hidden="true" /></span>
        <strong>{display(usage.total)}</strong>
        <small>Cached input: {display(usage.cached)}</small>
      </summary>
      <dl className="token-breakdown">
        <div><dt>Input</dt><dd>{display(usage.input)}</dd></div>
        <div><dt>Output</dt><dd>{display(usage.output)}</dd></div>
        <div><dt>Cached input</dt><dd>{display(usage.cached)}</dd></div>
        <div><dt>Reasoning output</dt><dd>{display(usage.reasoning)}</dd></div>
      </dl>
      <p className="token-note">{usage.cached === undefined
        ? "No cached-input count in stored usage. This does not mean zero cache hits."
        : "Cached input is part of input tokens; reasoning is part of output. These are upstream token counts, not proxy response-cache hits."}</p>
    </details>
  );
}

export function MessageList({ messages, expanded, onToggle }: { messages: unknown[]; expanded: Set<number>; onToggle: (index: number) => void }) {
  return (
    <div className="message-list">
      {messages.map((message, index) => {
        const record = isJsonRecord(message) ? message : null;
        const role = stringField(record, "role") || stringField(record, "type") || "item";
        const content = record && "content" in record ? record.content : message;
        const metadata = record ? omitFields(record, ["role", "content"]) : {};
        const preview = (typeof content === "string" && content ? content : prettyJSON(record?.tool_calls || content)).replace(/\s+/g, " ").slice(0, 160);
        const open = expanded.has(index);
        return (
          <article className={`message-card message-${safeClassName(role)}`} key={`${role}-${index}`}>
            <button type="button" className="message-toggle" aria-expanded={open} aria-label={`Message ${index + 1}: ${role}`} onClick={() => onToggle(index)}>
              <ChevronRight className={open ? "message-chevron expanded" : "message-chevron"} size={15} aria-hidden="true" />
              <Badge>{role}</Badge><span className="message-preview">{preview}</span><span>#{index + 1}</span>
            </button>
            {open && <div className="message-body">
              <MessageContent value={content} />
              {Object.keys(metadata).length > 0 && <JsonBlock value={metadata} />}
            </div>}
          </article>
        );
      })}
    </div>
  );
}

export function MessageContent({ value }: { value: unknown }) {
  if (typeof value === "string") return <pre className="message-text">{value || "(empty)"}</pre>;
  if (Array.isArray(value)) {
    return (
      <div className="message-parts">
        {value.map((part, index) => {
          const record = isJsonRecord(part) ? part : null;
          const text = record && typeof record.text === "string" ? record.text : null;
          const type = record && typeof record.type === "string" ? record.type : `part ${index + 1}`;
          return <div className="message-part" key={`${type}-${index}`}><Badge>{type}</Badge>{text !== null ? <pre className="message-text">{text}</pre> : <JsonBlock value={part} />}</div>;
        })}
      </div>
    );
  }
  return <JsonBlock value={value} />;
}

export function JsonDetailSection({ section }: { section: LogDetailSection }) {
  const [open, setOpen] = useState(Boolean(section.defaultOpen));
  return (
    <details className="json-detail-section" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <span>{section.title}</span>
        <Badge tone={section.tone}>{detailValueLabel(section.value)}</Badge>
      </summary>
      <JsonBlock value={section.value} />
    </details>
  );
}

export function JsonBlock({ value }: { value: unknown }) {
  return <pre className="json-block"><code>{prettyJSON(value)}</code></pre>;
}
