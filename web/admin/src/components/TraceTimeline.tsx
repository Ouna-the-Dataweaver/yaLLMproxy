import { Eye } from "lucide-react";
import { formatTime, formatValue } from "../lib/format";
import type { TraceTimelineItem } from "../types";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function TraceSummaryValue({ label, value }: { label: string; value: string | JSX.Element }) {
  return (
    <div className="trace-summary-value">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

export function TraceTimeline({ items, onOpenLog }: { items: TraceTimelineItem[]; onOpenLog?: (id: string) => Promise<void> }) {
  if (items.length === 0) {
    return <div className="empty-inline trace-empty">No trace events returned</div>;
  }
  return (
    <ol className="trace-timeline" aria-label="Trace event timeline">
      {items.map((item) => (
        <li className="trace-timeline-item" key={item.id}>
          <div className={`trace-marker trace-marker-${item.tone}`} aria-hidden="true" />
          <article className="trace-event">
            <div className="trace-event-header">
              <div>
                <Badge tone={item.tone}>{item.phase}</Badge>
                <strong>{item.title}</strong>
              </div>
              <div className="trace-event-actions">
                {item.time && <span>{formatTime(item.time)}</span>}
                {item.logID && onOpenLog && (
                  <Button type="button" variant="secondary" size="sm" onClick={() => void onOpenLog(item.logID || "")}>
                    <Eye size={15} aria-hidden="true" /> Inspect
                  </Button>
                )}
              </div>
            </div>
            <div className="trace-field-grid">
              {item.fields.map((field) => (
                <div className="trace-field" key={`${item.id}-${field.label}`}>
                  <span>{field.label}</span>
                  {field.tone ? <Badge tone={field.tone}>{formatValue(field.value)}</Badge> : <strong>{formatValue(field.value)}</strong>}
                </div>
              ))}
            </div>
          </article>
        </li>
      ))}
    </ol>
  );
}
