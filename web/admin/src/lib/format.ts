import type { JsonRecord } from "../types";
import { errorMessage } from "./api";

export function formatStringList(values: string[]) {
  return values.join("\n");
}

export function parseStringList(value: string) {
  return Array.from(
    new Set(
      value
        .split(/[,\n]/)
        .map((item) => item.trim())
        .filter(Boolean)
    )
  );
}

export function uniqueStrings(values: string[]) {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean)));
}

export function toggleStringSelection(values: string[], value: string, checked: boolean) {
  if (checked) {
    return uniqueStrings([...values, value]);
  }
  return values.filter((item) => item !== value);
}

export function arraysEqual(left: string[], right: string[]) {
  if (left.length !== right.length) return false;
  return left.every((item, index) => item === right[index]);
}

export function parseJSONField<T>(value: string, label: string, expected: "object" | "array"): { value: T; error?: string } {
  const fallback = expected === "array" ? [] : {};
  const trimmed = value.trim();
  if (!trimmed) return { value: fallback as T, error: `${label} is required; enter ${expected === "array" ? "[]" : "{}"} to clear it` };
  try {
    const parsed = JSON.parse(trimmed);
    if (expected === "array" && !Array.isArray(parsed)) {
      return { value: fallback as T, error: `${label} must be a JSON array` };
    }
    if (expected === "object" && !isJsonRecord(parsed)) {
      return { value: fallback as T, error: `${label} must be a JSON object` };
    }
    return { value: parsed as T };
  } catch (err) {
    return { value: fallback as T, error: errorMessage(err) };
  }
}

export function parseOptionalNumber(value: string, label: string, kind: "integer" | "number") {
  const trimmed = value.trim();
  if (!trimmed) return { value: undefined as number | undefined };
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed)) {
    return { value: undefined as number | undefined, error: `${label} must be a number` };
  }
  if (kind === "integer" && !Number.isSafeInteger(parsed)) {
    return { value: undefined as number | undefined, error: `${label} must be an integer` };
  }
  return { value: parsed };
}

export function formatNumberInput(value: unknown) {
  return typeof value === "number" && value !== 0 ? String(value) : "";
}

export function formatJSONText(value: unknown) {
  return JSON.stringify(value, null, 2);
}

export function jsonEqual(left: unknown, right: unknown) {
  return JSON.stringify(left ?? null) === JSON.stringify(right ?? null);
}

export function isEmptyObject(value: JsonRecord) {
  return Object.keys(value).length === 0;
}

export function titleCase(value: string) {
  return value.replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function isJsonRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

export function asArray<T>(value: unknown): T[] {
  return Array.isArray(value) ? (value as T[]) : [];
}

export function numberValue(value: unknown, fallback = 0) {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

export function formatBytes(value: unknown) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "-";
  if (value < 1024) return `${value} B`;
  const kib = value / 1024;
  if (kib < 1024) return `${kib.toFixed(1)} KiB`;
  const mib = kib / 1024;
  if (mib < 1024) return `${mib.toFixed(1)} MiB`;
  return `${(mib / 1024).toFixed(1)} GiB`;
}

export function stringField(value: unknown, key: string) {
  if (!isJsonRecord(value)) return "";
  return typeof value[key] === "string" ? value[key] : "";
}

export function numberField(value: unknown, key: string) {
  if (!isJsonRecord(value)) return undefined;
  return typeof value[key] === "number" ? value[key] : undefined;
}

export function nestedNumber(value: unknown, parent: string, key: string) {
  if (!isJsonRecord(value) || !isJsonRecord(value[parent])) return undefined;
  return typeof value[parent][key] === "number" ? value[parent][key] : undefined;
}

export function firstPresent(...values: unknown[]) {
  return values.find(hasDetailValue);
}

export function hasDetailValue(value: unknown) {
  if (value === undefined || value === null || value === "") return false;
  if (Array.isArray(value)) return value.length > 0;
  if (isJsonRecord(value)) return Object.keys(value).length > 0;
  return true;
}

export function parseJSONText(value: unknown) {
  if (typeof value !== "string") return value;
  const trimmed = value.trim();
  if (!trimmed) return "";
  try {
    return JSON.parse(trimmed) as unknown;
  } catch {
    return value;
  }
}

export function prettyJSON(value: unknown) {
  if (typeof value === "string") return value;
  if (value === undefined) return "-";
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function detailValueLabel(value: unknown) {
  if (Array.isArray(value)) return `${value.length} item${value.length === 1 ? "" : "s"}`;
  if (isJsonRecord(value)) {
    const count = Object.keys(value).length;
    return `${count} field${count === 1 ? "" : "s"}`;
  }
  if (typeof value === "string") return `${value.length} chars`;
  return "detail";
}

export function safeClassName(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "") || "item";
}

export async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand("copy");
  textarea.remove();
}

export function downloadJSON(value: unknown, filename: string) {
  const blob = new Blob([prettyJSON(value)], { type: "application/json;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function formatValue(value: unknown) {
  if (value === undefined || value === null || value === "") return "-";
  if (typeof value === "number") return Number.isInteger(value) ? value.toString() : value.toFixed(3);
  if (typeof value === "boolean") return value ? "true" : "false";
  if (typeof value === "object") return compactJSON(value);
  return String(value);
}

export function compactJSON(value: unknown) {
  if (value === undefined || value === null) return "-";
  return JSON.stringify(value);
}

export function percentValue(value: unknown) {
  if (typeof value !== "number") return formatValue(value);
  const normalized = value <= 1 ? value * 100 : value;
  return `${normalized.toFixed(1)}%`;
}

export function formatDuration(value?: number) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "-";
  if (value < 1000) return `${Math.round(value)} ms`;
  if (value < 60000) return `${(value / 1000).toFixed(value < 10000 ? 2 : 1)} s`;
  return `${Math.floor(value / 60000)}m ${Math.round((value % 60000) / 1000)}s`;
}

export function formatTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}
