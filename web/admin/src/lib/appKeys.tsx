import { AppKeyActions } from "../components/RowActions";
import { Badge } from "../components/ui/badge";
import type { AllowedKeysValue, AppKeyConfigForm, AppKeyForm, AppKeyMutationPayload, AppKeyRecord, AppKeysPayload, JsonRecord, LimitsRecord, SecretReveal } from "../types";
import { errorMessage } from "./api";
import { arraysEqual, asArray, formatJSONText, formatNumberInput, formatStringList, formatValue, isEmptyObject, isJsonRecord, jsonEqual, parseJSONField, parseOptionalNumber, parseStringList } from "./format";
import { renderBooleanBadge } from "./logs";
import { renderStringBadges } from "./routing";

export function appKeyFormFromRecord(key: AppKeyRecord): AppKeyForm {
  return {
    key_id: keyIDFromRecord(key),
    group_id: typeof key.group_id === "string" ? key.group_id : "",
    name: typeof key.name === "string" ? key.name : "",
    description: typeof key.description === "string" ? key.description : "",
    enabled: key.enabled !== false,
    models_text: formatStringList(asArray<string>(key.models)),
    concurrency_limit: formatNumberInput(key.concurrency_limit),
    priority: formatNumberInput(key.priority),
    queue_timeout: formatNumberInput(key.queue_timeout),
    limits_text: formatJSONText(isJsonRecord(key.limits) ? key.limits : {}),
    required_headers_text: formatJSONText(asArray<JsonRecord>(key.required_headers))
  };
}

export function appKeyConfigFormFromPayload(payload: AppKeysPayload): AppKeyConfigForm {
  return {
    enabled: Boolean(payload.enabled),
    header_name: typeof payload.header_name === "string" && payload.header_name.trim() ? payload.header_name : "x-app-key",
    allow_unauthenticated: Boolean(payload.allow_unauthenticated),
    defaults_text: formatJSONText(isJsonRecord(payload.defaults) ? payload.defaults : {}),
    unauthenticated_text: formatJSONText(isJsonRecord(payload.unauthenticated) ? payload.unauthenticated : {}),
    groups_text: formatJSONText(appKeyGroups(payload)),
    required_headers_text: formatJSONText(asArray<JsonRecord>(payload.required_headers))
  };
}

export function appKeyPayloadFromForm(form: AppKeyForm, original: AppKeyRecord | null, update: boolean) {
  const errors: Partial<Record<keyof AppKeyForm, string>> = {};
  const payload: JsonRecord = {};
  const textFields: Array<keyof Pick<AppKeyForm, "group_id" | "name" | "description">> = ["group_id", "name", "description"];

  if (!update && form.key_id.trim()) {
    payload.key_id = form.key_id.trim();
  }
  if (!update) {
    payload.enabled = form.enabled;
  } else if (form.enabled !== (original?.enabled !== false)) {
    payload.enabled = form.enabled;
  }

  textFields.forEach((field) => {
    const value = form[field].trim();
    const originalValue = original && typeof original[field] === "string" ? String(original[field]) : "";
    if (!update) {
      if (value) payload[field] = value;
    } else if (value !== originalValue) {
      payload[field] = value;
    }
  });

  const models = parseStringList(form.models_text);
  if (!update) {
    if (models.length > 0) payload.models = models;
  } else if (!arraysEqual(asArray<string>(original?.models), models)) {
    payload.models = models;
  }

  addNumericPayloadField(payload, errors, form, original, "concurrency_limit", "integer", update);
  addNumericPayloadField(payload, errors, form, original, "priority", "integer", update);
  addNumericPayloadField(payload, errors, form, original, "queue_timeout", "number", update);

  const limits = parseJSONField<LimitsRecord>(form.limits_text, "Limits JSON", "object");
  if (limits.error) {
    errors.limits_text = limits.error;
  } else if (!update) {
    if (!isEmptyObject(limits.value)) payload.limits = limits.value;
  } else if (!jsonEqual(isJsonRecord(original?.limits) ? original?.limits : {}, limits.value)) {
    payload.limits = limits.value;
  }

  const requiredHeaders = parseJSONField<JsonRecord[]>(form.required_headers_text, "Required headers JSON", "array");
  if (requiredHeaders.error) {
    errors.required_headers_text = requiredHeaders.error;
  } else if (!update) {
    if (requiredHeaders.value.length > 0) payload.required_headers = requiredHeaders.value;
  } else if (!jsonEqual(asArray<JsonRecord>(original?.required_headers), requiredHeaders.value)) {
    payload.required_headers = requiredHeaders.value;
  }

  return { payload, errors };
}

export function addNumericPayloadField(
  payload: JsonRecord,
  errors: Partial<Record<keyof AppKeyForm, string>>,
  form: AppKeyForm,
  original: AppKeyRecord | null,
  field: "concurrency_limit" | "priority" | "queue_timeout",
  kind: "integer" | "number",
  update: boolean
) {
  const parsed = parseOptionalNumber(form[field], field.replace(/_/g, " "), kind);
  if (parsed.error) {
    errors[field] = parsed.error;
    return;
  }
  const originalValue = typeof original?.[field] === "number" ? Number(original[field]) : 0;
  const nextValue = parsed.value ?? 0;
  if (!update) {
    if (parsed.value !== undefined) payload[field] = nextValue;
  } else if (nextValue !== originalValue) {
    payload[field] = nextValue;
  }
}

export function appKeysConfigPayloadFromForm(form: AppKeyConfigForm) {
  const errors: Partial<Record<keyof AppKeyConfigForm, string>> = {};
  const defaults = parseJSONField<LimitsRecord>(form.defaults_text, "Defaults JSON", "object");
  const unauthenticated = parseJSONField<LimitsRecord>(form.unauthenticated_text, "Unauthenticated JSON", "object");
  const groups = parseJSONField<JsonRecord[]>(form.groups_text, "Groups JSON", "array");
  const requiredHeaders = parseJSONField<JsonRecord[]>(form.required_headers_text, "Required headers JSON", "array");
  if (!form.header_name.trim()) errors.header_name = "Header name is required";
  if (defaults.error) errors.defaults_text = defaults.error;
  if (unauthenticated.error) errors.unauthenticated_text = unauthenticated.error;
  if (groups.error) errors.groups_text = groups.error;
  if (requiredHeaders.error) errors.required_headers_text = requiredHeaders.error;
  return {
    payload: {
      enabled: form.enabled,
      header_name: form.header_name.trim(),
      allow_unauthenticated: form.allow_unauthenticated,
      defaults: defaults.value,
      unauthenticated: unauthenticated.value,
      groups: groups.value,
      required_headers: requiredHeaders.value
    },
    errors
  };
}

export function parseAllowedKeysField(value: string, label: string): { value: AllowedKeysValue; error?: string } {
  const trimmed = value.trim();
  if (!trimmed) return { value: [], error: `${label} is required; enter [] to clear it` };
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (typeof parsed === "string") {
      return { value: parsed };
    }
    if (Array.isArray(parsed) && parsed.every((item) => typeof item === "string" && item.trim().length > 0)) {
      return { value: parsed };
    }
    return { value: [], error: `${label} must be a JSON string or an array of non-empty strings` };
  } catch (err) {
    return { value: [], error: errorMessage(err) };
  }
}

export function secretRevealFromResponse(response: AppKeyMutationPayload): SecretReveal {
  return {
    keyId: String(response.key_id || response.key?.key_id || ""),
    secret: String(response.secret || ""),
    message: String(response.message || "Store this secret securely - it will not be shown again")
  };
}

export function renderAppKeyCell(
  row: JsonRecord,
  column: string,
  disabled: boolean,
  onEdit: (key: AppKeyRecord) => void,
  onRegenerate: (keyID?: string) => Promise<void>,
  onDelete: (keyID?: string) => Promise<void>
) {
  const keyID = keyIDFromRecord(row);
  if (column === "key_id") return <span className="resource-name">{keyID || "-"}</span>;
  if (column === "enabled") return renderBooleanBadge(row.enabled !== false);
  if (column === "has_secret") return <Badge tone={row.has_secret ? "good" : "warn"}>{row.has_secret ? "stored" : "missing"}</Badge>;
  if (column === "models") return renderStringBadges(asArray<string>(row.models));
  if (column === "actions") return <AppKeyActions row={row as AppKeyRecord} keyID={keyID} disabled={disabled} onEdit={onEdit} onRegenerate={onRegenerate} onDelete={onDelete} />;
  return formatValue(row[column]);
}

export function appKeyGroups(payload: AppKeysPayload) {
  return asArray<JsonRecord>(payload.groups);
}

export function keyIDFromRecord(row: AppKeyRecord | JsonRecord) {
  return typeof row.key_id === "string" ? row.key_id : "";
}
