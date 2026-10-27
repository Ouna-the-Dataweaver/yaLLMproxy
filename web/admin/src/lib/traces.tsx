import { Badge } from "../components/ui/badge";
import type { BadgeTone, TraceAttemptRecord, TraceDetailPayload, TraceErrorRecord, TraceRequestRecord, TraceTimelineField, TraceTimelineItem } from "../types";
import { APIError } from "./api";
import { asArray, formatDuration, formatTime, formatValue, isJsonRecord, isNonEmptyString, uniqueStrings } from "./format";
import { renderBooleanBadge } from "./logs";

export function traceRequestsFromPayload(detail: TraceDetailPayload | null) {
  if (!detail) return [];
  const requests = firstTraceArray<TraceRequestRecord>(detail.requests, detail.logs, detail.data);
  return requests;
}

export function traceAttemptsFromPayload(detail: TraceDetailPayload | null) {
  if (!detail) return [];
  return firstTraceArray<TraceAttemptRecord>(detail.attempts, detail.attempt_trail);
}

export function traceErrorsFromPayload(detail: TraceDetailPayload | null) {
  if (!detail) return [];
  return firstTraceArray<TraceErrorRecord>(detail.errors, detail.error_logs);
}

export function firstTraceArray<T>(...values: Array<T[] | undefined>) {
  return values.find((value) => Array.isArray(value) && value.length > 0) || [];
}

export function traceTimelineItems(detail: TraceDetailPayload | null) {
  const requests = traceRequestsFromPayload(detail);
  const attempts = traceAttemptsFromPayload(detail);
  const errors = traceErrorsFromPayload(detail);
  const attemptsByRequest = groupTraceRecords(attempts, (attempt) => attempt.request_id);
  const errorsByRequest = groupTraceRecords(errors, (error) => error.request_log_id);
  const usedAttempts = new Set<TraceAttemptRecord>();
  const usedErrors = new Set<TraceErrorRecord>();
  const items: TraceTimelineItem[] = [];

  requests.forEach((request, index) => {
    const requestID = traceRequestID(request);
    items.push(traceRequestTimelineItem(request, index));
    for (const attempt of attemptsByRequest.get(requestID) || []) {
      usedAttempts.add(attempt);
      items.push(traceAttemptTimelineItem(attempt, items.length));
    }
    for (const errorRecord of errorsByRequest.get(requestID) || []) {
      usedErrors.add(errorRecord);
      items.push(traceErrorTimelineItem(errorRecord, items.length));
    }
  });

  attempts.forEach((attempt) => {
    if (!usedAttempts.has(attempt)) {
      items.push(traceAttemptTimelineItem(attempt, items.length));
    }
  });
  errors.forEach((errorRecord) => {
    if (!usedErrors.has(errorRecord)) {
      items.push(traceErrorTimelineItem(errorRecord, items.length));
    }
  });

  return items;
}

export function groupTraceRecords<T>(records: T[], keyForRecord: (record: T) => string | undefined) {
  const groups = new Map<string, T[]>();
  records.forEach((record) => {
    const key = (keyForRecord(record) || "").trim();
    if (!key) return;
    const group = groups.get(key) || [];
    group.push(record);
    groups.set(key, group);
  });
  return groups;
}

export function traceRequestTimelineItem(request: TraceRequestRecord, index: number): TraceTimelineItem {
  const endpointKind = endpointKindFromPath(request.path) || endpointKindFromPath(request.request_path);
  const method = traceMethodLabel(request.method);
  const outcome = safeTraceCategory(request.outcome);
  return {
    id: `trace-request-${traceRequestID(request) || index}`,
    phase: "request",
    title: `${method} ${endpointKind || "request"}`,
    tone: traceOutcomeTone(outcome, request.backend_status ?? request.status),
    time: request.request_time || request.created_at,
    logID: traceRequestID(request) || undefined,
    fields: traceFields([
      ["Endpoint", endpointKind || "other"],
      ["Model", request.model_name || request.model],
      ["Backend", request.backend_name],
      ["Status", request.backend_status ?? request.status, httpStatusTone(request.backend_status ?? request.status)],
      ["Outcome", outcome, traceOutcomeTone(outcome, request.backend_status ?? request.status)],
      ["Attempts", request.attempt_count],
      ["Warnings", request.warning_count, numberTone(request.warning_count)],
      ["Errors", request.error_count, numberTone(request.error_count, "bad")],
      ["Tokens", traceTokenSummary(request)],
      ["Stop reason", request.stop_reason],
      ["Duration", formatDuration(request.duration_ms)],
      ["Stream", typeof request.is_stream === "boolean" ? (request.is_stream ? "yes" : "no") : ""],
      ["Tool call", typeof request.is_tool_call === "boolean" ? (request.is_tool_call ? "yes" : "no") : ""],
      ["Key scope", request.key_scope],
      ["Span", request.span_id]
    ])
  };
}

export function traceAttemptTimelineItem(attempt: TraceAttemptRecord, index: number): TraceTimelineItem {
  const attemptLabel = typeof attempt.attempt_index === "number" ? `attempt ${attempt.attempt_index}` : "attempt";
  const outcome = safeTraceCategory(attempt.outcome);
  const errorType = safeTraceCategory(attempt.error_type);
  return {
    id: `trace-attempt-${attempt.request_id || "orphan"}-${attempt.attempt_index ?? index}-${index}`,
    phase: "upstream",
    title: attempt.backend_name || attemptLabel,
    tone: traceOutcomeTone(outcome, attempt.status),
    fields: traceFields([
      ["Request", attempt.request_id],
      ["Backend", attempt.backend_name],
      ["Attempt", attempt.attempt_index],
      ["Retry", attempt.retry_index],
      ["Status", attempt.status, httpStatusTone(attempt.status)],
      ["Outcome", outcome, traceOutcomeTone(outcome, attempt.status)],
      ["Retryable", typeof attempt.retryable === "boolean" ? (attempt.retryable ? "yes" : "no") : ""],
      ["Duration", formatDuration(attempt.duration_ms)],
      ["Error type", errorType, errorType ? "warn" : undefined]
    ])
  };
}

export function traceErrorTimelineItem(errorRecord: TraceErrorRecord, index: number): TraceTimelineItem {
  const errorType = safeTraceCategory(errorRecord.error_type);
  return {
    id: `trace-error-${errorRecord.id || errorRecord.request_log_id || index}-${index}`,
    phase: "error",
    title: errorType || "linked error",
    tone: "bad",
    time: errorRecord.timestamp || errorRecord.created_at,
    fields: traceFields([
      ["Request", errorRecord.request_log_id],
      ["Type", errorType, errorType ? "bad" : undefined],
      ["Model", errorRecord.model_name],
      ["Backend", errorRecord.backend_name],
      ["HTTP status", errorRecord.http_status, httpStatusTone(errorRecord.http_status)]
    ])
  };
}

export function traceFields(entries: Array<[string, unknown, BadgeTone?]>): TraceTimelineField[] {
  return entries
    .filter(([, value]) => value !== undefined && value !== null && value !== "")
    .map(([label, value, tone]) => ({ label, value, tone }));
}

export function traceRequestID(request: TraceRequestRecord) {
  return typeof request.id === "string" ? request.id : "";
}

export function traceMethodLabel(value: unknown) {
  if (typeof value !== "string") return "REQUEST";
  const trimmed = value.trim();
  const method = trimmed.toUpperCase();
  return /^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$/.test(method) ? method : "REQUEST";
}

export function safeTraceCategory(value: unknown) {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  if (!trimmed) return "";
  if (trimmed.length > 80 || /^https?:\/\//i.test(trimmed) || /[\s/?#&=]/.test(trimmed)) return "present";
  return /^[A-Za-z0-9_.-]+$/.test(trimmed) ? trimmed : "present";
}

export function endpointKindFromPath(value: unknown) {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  if (!trimmed || /^https?:\/\//i.test(trimmed)) return "";
  const normalized = trimmed.split("?")[0].trim().toLowerCase();
  if (normalized.includes("/chat/completions")) return "chat";
  if (normalized.includes("/embeddings")) return "embeddings";
  if (normalized.includes("/rerank")) return "rerank";
  if (normalized.includes("/messages")) return "messages";
  if (normalized.includes("/responses")) return "responses";
  if (normalized.startsWith("/p1/")) return "passthrough";
  return "";
}

export function traceTokenSummary(request: TraceRequestRecord) {
  const parts = [
    tokenPart("prompt", request.prompt_tokens),
    tokenPart("completion", request.completion_tokens),
    tokenPart("cached", request.cached_tokens),
    tokenPart("reasoning", request.reasoning_tokens),
    tokenPart("total", request.total_tokens)
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" / ") : "";
}

export function tokenPart(label: string, value: unknown) {
  return typeof value === "number" && value > 0 ? `${label} ${value}` : "";
}

export function renderTraceAttemptCell(row: TraceAttemptRecord, column: string) {
  if (column === "status") return row.status ? <Badge tone={httpStatusTone(row.status)}>{row.status}</Badge> : "-";
  if (column === "outcome") {
    const outcome = safeTraceCategory(row.outcome);
    return outcome ? <Badge tone={traceOutcomeTone(outcome, row.status)}>{outcome}</Badge> : "-";
  }
  if (column === "retryable") return renderBooleanBadge(Boolean(row.retryable));
  if (column === "duration_ms") return formatDuration(row.duration_ms);
  if (column === "error_type") {
    const errorType = safeTraceCategory(row.error_type);
    return errorType ? <Badge tone="warn">{errorType}</Badge> : "-";
  }
  if (column === "attempt_index" && typeof row.attempt_index === "number") return formatValue(row.attempt_index);
  return formatValue(row[column as keyof TraceAttemptRecord]);
}

export function renderTraceErrorCell(row: TraceErrorRecord, column: string) {
  if (column === "timestamp" || column === "created_at") return formatTime(row[column]);
  if (column === "error_type") {
    const errorType = safeTraceCategory(row.error_type);
    return errorType ? <Badge tone="bad">{errorType}</Badge> : "-";
  }
  if (column === "http_status") return row.http_status ? <Badge tone={httpStatusTone(row.http_status)}>{row.http_status}</Badge> : "-";
  return formatValue(row[column as keyof TraceErrorRecord]);
}

export function renderTraceOutcomeBadges(values: string[]) {
  const outcomes = uniqueStrings(values.map(safeTraceCategory).filter(isNonEmptyString));
  if (outcomes.length === 0) return "-";
  return (
    <span className="badge-stack">
      {outcomes.map((value) => (
        <Badge key={value} tone={traceOutcomeTone(value)}>{value}</Badge>
      ))}
    </span>
  );
}

export function traceSafeErrorMessage(err: unknown) {
  if (err instanceof APIError) {
    if (err.status === 401 || err.status === 403) return "Admin password is required or invalid for trace navigation.";
    if (err.status === 400) return "Trace ID is invalid.";
    if (err.status === 404) return "Trace was not found.";
    return "Trace details are unavailable.";
  }
  return "Trace details are unavailable.";
}

export function traceStatusMessage(error: string, status: number | null, detail: TraceDetailPayload | null, loading: boolean) {
  if (loading && !detail) return "Loading trace.";
  if (error) return error;
  if (!detail) return "No trace loaded.";
  if (detail.has_more) return "Trace has more records than returned.";
  return "";
}

export function traceStatusLabel(error: string, status: number | null, detail: TraceDetailPayload | null, loading: boolean) {
  if (loading && !detail) return "loading";
  if (error) {
    if (status === 401 || status === 403) return "auth";
    if (status === 404) return "missing";
    if (status === 400) return "invalid";
    return "error";
  }
  if (!detail) return "empty";
  if (detail.has_more) return "partial";
  return "ready";
}

export function traceStatusTone(error: string, status: number | null, detail: TraceDetailPayload | null, loading: boolean): BadgeTone {
  if (loading && !detail) return "warn";
  if (error) return status === 404 ? "warn" : "bad";
  if (!detail) return "neutral";
  if (detail.has_more) return "warn";
  return "good";
}

export function numberTone(value: unknown, nonZeroTone: BadgeTone = "warn") {
  return typeof value === "number" && value > 0 ? nonZeroTone : undefined;
}

export function httpStatusTone(status: unknown): BadgeTone {
  if (typeof status !== "number") return "neutral";
  if (status >= 500) return "bad";
  if (status >= 400) return "warn";
  if (status >= 200) return "good";
  return "neutral";
}

export function traceOutcomeTone(outcome?: unknown, status?: unknown): BadgeTone {
  if (outcome === "success") return "good";
  if (outcome === "error" || outcome === "failed") return "bad";
  if (outcome === "warning" || outcome === "retry") return "warn";
  return httpStatusTone(status);
}

export function traceIDFromRecord(value: unknown) {
  if (!isJsonRecord(value)) return "";
  const candidates = [value.trace_id, value.traceId, value.traceID, value.request_trace_id];
  const match = candidates.find((candidate) => typeof candidate === "string" && candidate.trim().length > 0);
  return typeof match === "string" ? match.trim() : "";
}

export function extractTraceID(payloads: unknown[]) {
  for (const payload of payloads) {
    const direct = traceIDFromRecord(payload);
    if (direct) return direct;
    if (isJsonRecord(payload)) {
      const nested = asArray<unknown>(payload.data).map(traceIDFromRecord).find(Boolean);
      if (nested) return nested;
    }
  }
  return "";
}

export function renderTraceID(value: string, onOpen?: (traceID: string) => Promise<void>) {
  if (!value) return "-";
  if (!onOpen) return <Badge>{value}</Badge>;
  return (
    <button
      type="button"
      className="trace-id-button"
      onClick={(event) => {
        event.stopPropagation();
        void onOpen(value);
      }}
      title="Open trace"
    >
      <Badge>{value}</Badge>
    </button>
  );
}
