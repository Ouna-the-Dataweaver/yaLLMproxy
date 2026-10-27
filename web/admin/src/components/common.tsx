import { Activity, Copy, Trash2, X } from "lucide-react";
import type { JsonRecord, SecretReveal, ValidationIssue } from "../types";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function Metric({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) {
  return (
    <section className="metric">
      <span className="metric-label">
        <Icon size={15} aria-hidden="true" />
        {label}
      </span>
      <strong className="metric-value">{value}</strong>
    </section>
  );
}

export function ValidationIssues({ issues, summary }: { issues: ValidationIssue[]; summary: { errors: number; warnings: number } }) {
  if (issues.length === 0) {
    return (
      <div className="validation-summary">
        <Badge tone="good">No validation issues</Badge>
      </div>
    );
  }
  return (
    <div className="validation-list">
      <div className="validation-summary">
        <Badge tone={summary.errors > 0 ? "bad" : "good"}>{summary.errors} errors</Badge>
        <Badge tone={summary.warnings > 0 ? "warn" : "neutral"}>{summary.warnings} warnings</Badge>
      </div>
      {issues.map((issue, index) => (
        <div className="validation-row" key={`${issue.path}-${issue.code}-${index}`}>
          <Badge tone={issue.severity === "error" ? "bad" : "warn"}>{issue.severity}</Badge>
          <code>{issue.path}</code>
          <span>{issue.message}</span>
        </div>
      ))}
    </div>
  );
}

export function FieldError({ message }: { message?: string }) {
  if (!message) return null;
  return <span className="field-error">{message}</span>;
}

export function PanelTitle({ icon: Icon, title }: { icon: typeof Activity; title: string }) {
  return (
    <div className="panel-title">
      <Icon size={18} aria-hidden="true" />
      <h3>{title}</h3>
    </div>
  );
}

export function DataTable({
  columns,
  rows,
  empty,
  renderCell
}: {
  columns: string[];
  rows: JsonRecord[];
  empty: string;
  renderCell: (row: JsonRecord, column: string) => string | JSX.Element;
}) {
  const numeric = new Set(columns.filter((column) => isNumericColumn(rows, column)));
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column} className={numeric.has(column) ? "num" : undefined}>{columnLabel(column)}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={String(row.id || row.name || row.model_name || index)}>
              {columns.map((column) => (
                <td key={column} className={numeric.has(column) ? "num" : undefined}>{renderCell(row, column)}</td>
              ))}
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={columns.length} className="empty-cell">{empty}</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

// columnLabel turns an API field key such as "last_error" into "Last error".
const acronyms: Record<string, string> = { api: "API", id: "ID", url: "URL", rps: "RPS", rpm: "RPM", ttl: "TTL", http: "HTTP" };

export function columnLabel(column: string) {
  const words = column.replace(/_/g, " ").trim().split(/\s+/).map((word) => acronyms[word.toLowerCase()] ?? word);
  const text = words.join(" ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function isNumericColumn(rows: JsonRecord[], column: string) {
  let seen = false;
  for (const row of rows) {
    const value = row[column];
    if (value === undefined || value === null || value === "") continue;
    if (typeof value !== "number") return false;
    seen = true;
  }
  return seen;
}

export function StatList({ rows, render, empty }: { rows: JsonRecord[]; render: (row: JsonRecord) => string | JSX.Element; empty: string }) {
  if (rows.length === 0) return <div className="empty-block">{empty}</div>;
  return (
    <dl className="stat-list">
      {rows.map((row) => (
        <div key={String(row.id)} className="stat-item">
          <dt>{columnLabel(String(row.field))}</dt>
          <dd>{render(row)}</dd>
        </div>
      ))}
    </dl>
  );
}

export function SecretBanner({ reveal, onDismiss }: { reveal: SecretReveal; onDismiss: () => void }) {
  async function copySecret() {
    await navigator.clipboard?.writeText(reveal.secret);
  }

  return (
    <section className="secret-banner">
      <div>
        <Badge tone="warn">One-time secret</Badge>
        <strong>{reveal.keyId}</strong>
        <span>{reveal.message}</span>
      </div>
      <code>{reveal.secret}</code>
      <div className="toolbar">
        <Button type="button" variant="secondary" size="icon" onClick={() => void copySecret()} title="Copy secret">
          <Copy size={16} aria-hidden="true" />
        </Button>
        <Button type="button" variant="ghost" size="icon" onClick={onDismiss} title="Dismiss secret">
          <X size={16} aria-hidden="true" />
        </Button>
      </div>
    </section>
  );
}

export function DeleteButton({ name, disabled, onDelete }: { name?: string; disabled: boolean; onDelete: (name?: string) => Promise<void> }) {
  return (
    <>
      <Button variant="danger" size="icon" disabled={disabled || !name} onClick={() => void onDelete(name)} title={name ? `Delete ${name}` : "Delete"}>
        <Trash2 size={16} aria-hidden="true" />
      </Button>
    </>
  );
}
