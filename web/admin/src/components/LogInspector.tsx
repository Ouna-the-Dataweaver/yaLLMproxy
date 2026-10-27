import { Activity, ChevronLeft, ChevronRight, Copy, Download, Eye, FileJson, Search } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { copyText, detailValueLabel, downloadJSON, firstPresent, formatDuration, formatTime, formatValue, hasDetailValue, isJsonRecord, nestedNumber, numberField, parseJSONText, prettyJSON, safeClassName, stringField } from "../lib/format";
import { conversationMessages, logDetailSections, outcomeTone } from "../lib/logs";
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

  return (
    <div className="log-inspector">
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
      </div>

      <div className="log-overview-grid">
        <TraceSummaryValue label="Time" value={formatTime(stringField(log, "request_time") || stringField(log, "created_at"))} />
        <TraceSummaryValue label="Model" value={formatValue(log.model_name)} />
        <TraceSummaryValue label="Route" value={`${formatValue(log.method)} ${formatValue(log.path || log.request_path)}`} />
        <TraceSummaryValue label="Outcome" value={<Badge tone={outcomeTone(log as LogRecord)}>{formatValue(log.outcome || log.backend_status)}</Badge>} />
        <TraceSummaryValue label="Backend" value={formatValue(log.backend_name)} />
        <TraceSummaryValue label="Duration" value={formatDuration(numberField(log, "duration_ms"))} />
        <TraceSummaryValue label="Tokens" value={formatValue(log.total_tokens || nestedNumber(log, "usage_stats", "total_tokens"))} />
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
              <MessageList messages={messages} />
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

export function MessageList({ messages }: { messages: unknown[] }) {
  return (
    <div className="message-list">
      {messages.map((message, index) => {
        const record = isJsonRecord(message) ? message : null;
        const role = stringField(record, "role") || stringField(record, "type") || "item";
        const content = record && "content" in record ? record.content : message;
        return (
          <article className={`message-card message-${safeClassName(role)}`} key={`${role}-${index}`}>
            <header><Badge>{role}</Badge><span>#{index + 1}</span></header>
            <MessageContent value={content} />
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
