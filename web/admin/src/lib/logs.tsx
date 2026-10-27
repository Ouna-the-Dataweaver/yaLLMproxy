import { Badge } from "../components/ui/badge";
import type { BadgeTone, JsonRecord, LogDetailSection, LogMeta, LogRecord } from "../types";
import { firstPresent, formatValue, hasDetailValue, isJsonRecord, parseJSONText, percentValue } from "./format";

export function quotaScopes(usage: JsonRecord): JsonRecord[] {
  const quota = usage.quota as JsonRecord | undefined;
  const used = quota?.used_by_scope as JsonRecord | undefined;
  if (!used) return [];
  return Object.entries(used).map(([scope, value], index) => ({ scope: quotaScopeLabel(scope, index), used: value }));
}

export function quotaScopeLabel(scope: string, index: number) {
  if (scope === "__unauthenticated__") return "unauthenticated";
  return `quota scope ${index + 1}`;
}

export function renderHealthCell(row: JsonRecord, column: string) {
  if (column === "healthy") {
    return <Badge tone={row.healthy === false ? "bad" : "good"}>{row.healthy === false ? "down" : "up"}</Badge>;
  }
  if (column === "score") {
    return percentValue(row[column]);
  }
  if (column === "last_error") {
    return row[column] ? <Badge tone="warn">present</Badge> : "-";
  }
  return formatValue(row[column]);
}

export function renderBooleanBadge(value: unknown) {
  return <Badge tone={value ? "good" : "neutral"}>{value ? "yes" : "no"}</Badge>;
}

export function outcomeTone(row: LogRecord) {
  if (row.outcome === "success" || (row.backend_status && row.backend_status < 400)) return "good";
  if (row.outcome === "error" || (row.backend_status && row.backend_status >= 500)) return "bad";
  if (row.backend_status && row.backend_status >= 400) return "warn";
  return "neutral";
}

export function stopReasonTone(reason?: string): BadgeTone {
  if (reason === "stop" || reason === "tool_calls" || reason === "function_call") return "good";
  if (reason === "length") return "warn";
  if (reason === "content_filter") return "bad";
  return "neutral";
}

export function tokenDetailTitle(row: LogRecord) {
  const usage = row.usage_stats || {};
  const parts = [
    ["Input", row.prompt_tokens ?? usage.prompt_tokens],
    ["Output", row.completion_tokens ?? usage.completion_tokens],
    ["Cached", row.cached_tokens ?? usage.cached_tokens],
    ["Reasoning", row.reasoning_tokens ?? usage.reasoning_tokens]
  ].filter(([, value]) => typeof value === "number");
  return parts.map(([label, value]) => `${label}: ${value}`).join(" | ");
}

export function formatLogRange(meta: LogMeta, rowCount: number) {
  if (meta.total <= 0 || rowCount <= 0) return "No logs";
  const start = meta.offset + 1;
  const end = meta.offset + rowCount;
  return `${start}-${end} of ${meta.total}`;
}

export function dateTimeFilterValue(value: string) {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toISOString();
}

export function logDetailSections(log: JsonRecord | null): LogDetailSection[] {
  if (!log) return [];
  const sections: LogDetailSection[] = [];
  const body = firstPresent(log.request_body, log.body);
  const bodyRecord = isJsonRecord(body) ? body : null;
  const messages = conversationMessages(bodyRecord);
  const response = firstPresent(log.response_body, parseJSONText(log.full_response));

  const add = (id: string, title: string, value: unknown, options: Pick<LogDetailSection, "defaultOpen" | "tone"> = {}) => {
    if (hasDetailValue(value)) sections.push({ id, title, value, ...options });
  };

  add("system", "System prompt", bodyRecord?.system, { defaultOpen: true });
  if (messages.length === 0) add("request", "Request body", body, { defaultOpen: true });
  add("tools", "Tools", firstPresent(bodyRecord?.tools, bodyRecord?.functions));
  add("tool-choice", "Tool choice", bodyRecord?.tool_choice);
  if (bodyRecord) {
    add("request-parameters", "Other request parameters", omitFields(bodyRecord, ["messages", "input", "system", "tools", "functions", "tool_choice"]));
  }
  add("response", "Response", response, { defaultOpen: true });
  add("tool-calls", "Response tool calls", log.tool_calls, { defaultOpen: true });
  add("parsed-response", "Parsed upstream response", log.parsed_response);
  add("parsed-stream", "Parsed stream", log.parsed_stream);
  add("stream-chunks", "Stream chunks", log.stream_chunks);
  add("usage", "Usage statistics", log.usage_stats);
  add("modules", "Module events", log.modules_log);
  add("attempts", "Backend attempts", firstPresent(log.attempt_trail, log.attempts, log.backend_attempts), { tone: "warn" });
  add("errors", "Errors", log.errors, { defaultOpen: true, tone: "bad" });
  add("warnings", "Warnings", log.warnings, { defaultOpen: true, tone: "warn" });
  add("error-logs", "Linked error logs", log.error_logs, { defaultOpen: true, tone: "bad" });
  add("request-headers", "Request headers", log.request_headers);
  add("response-headers", "Response headers", log.response_headers);
  add("required-headers", "Required headers", log.required_headers);
  return sections;
}

export function omitFields(record: JsonRecord, fields: string[]) {
  const omitted = new Set(fields);
  return Object.fromEntries(Object.entries(record).filter(([key]) => !omitted.has(key)));
}

export function conversationMessages(body: JsonRecord | null) {
  if (!body) return [];
  if (Array.isArray(body.messages)) return body.messages;
  if (Array.isArray(body.input) && body.input.some((item) => isJsonRecord(item) && (typeof item.role === "string" || typeof item.type === "string"))) {
    return body.input;
  }
  return [];
}
