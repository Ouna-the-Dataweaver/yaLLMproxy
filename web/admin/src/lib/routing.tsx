import { ModelTreeActions, RoutingActions } from "../components/RowActions";
import { Badge } from "../components/ui/badge";
import type { ClientModelEditForm, ClientModelForm, ClientModelRecord, FallbackRow, JsonRecord, LimitsRecord, ModelForm, ModelTreeNode, ModelTreePayload, NodeForm, NodeRecord, ParsedNodeWeights, ProviderForm, ProviderRecord, SelectedProviderModel } from "../types";
import { APIError, apiErrorPayload, errorMessage, payloadErrorCode } from "./api";
import { arraysEqual, asArray, formatValue, isJsonRecord, isNonEmptyString, parseJSONField, parseStringList, uniqueStrings } from "./format";
import { renderBooleanBadge } from "./logs";

export function validateModelForm(form: ModelForm) {
  const errors: Partial<Record<keyof ModelForm, string>> = {};
  if (!form.model_name.trim()) {
    errors.model_name = "Model name is required";
  }
  if (!form.api_base.trim()) {
    errors.api_base = "API base is required";
  } else {
    try {
      const url = new URL(form.api_base);
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        errors.api_base = "API base must be http or https";
      }
    } catch {
      errors.api_base = "API base must be an absolute URL";
    }
  }
  if (!["openai", "anthropic", "gigachat", "passthrough"].includes(form.api_type)) {
    errors.api_type = "Choose a supported API type";
  }
  if (form.api_type === "gigachat") {
    const hasBasicKey = Boolean(form.api_key.trim());
    const hasClientCredentials = Boolean(form.client_id.trim() && form.client_secret.trim());
    const hasLocalCredentials = Boolean(form.client_cert.trim() && form.client_key.trim());
    if (form.mode === "cloud" && !hasBasicKey && !hasClientCredentials) {
      errors.client_secret = "Cloud mode needs an API key or client credentials";
    }
    if (form.mode === "local" && !hasLocalCredentials) {
      errors.client_cert = "Local mode needs a client cert and key";
    }
    if (!form.mode && !hasBasicKey && !hasClientCredentials && !hasLocalCredentials) {
      errors.api_key = "GigaChat needs cloud or local credentials";
    }
    if (form.auth_url.trim()) {
      try {
        const url = new URL(form.auth_url);
        if (url.protocol !== "http:" && url.protocol !== "https:") {
          errors.auth_url = "Auth URL must be http or https";
        }
      } catch {
        errors.auth_url = "Auth URL must be an absolute URL";
      }
    }
  }
  return errors;
}

export function parseProviderModels(text: string): SelectedProviderModel[] {
  const seen = new Set<string>();
  return text.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => {
    const parts = line.split("=").map((part) => part.trim());
    if (parts.length > 2 || !parts[0] || (parts.length === 2 && !parts[1])) throw new Error("Use one model per line, or public-name = upstream-name");
    if (seen.has(parts[0])) throw new Error(`Duplicate model: ${parts[0]}`);
    seen.add(parts[0]);
    return parts.length === 2 ? { name: parts[0], upstream_model: parts[1] } : { name: parts[0] };
  });
}

export function formatProviderModels(models: unknown): string {
  return asArray<string | SelectedProviderModel>(models).map((model) => typeof model === "string" ? model : model.upstream_model ? `${model.name} = ${model.upstream_model}` : model.name).join("\n");
}

export function validateProviderForm(form: ProviderForm) {
  const errors: Partial<Record<keyof ProviderForm, string>> = {};
  try { parseProviderModels(form.models_text); } catch (err) { errors.models_text = errorMessage(err); }
  const limits = parseJSONField<LimitsRecord>(form.limits_text, "Limits JSON", "object");
  if (limits.error) errors.limits_text = limits.error;
  if (!form.name.trim()) {
    errors.name = "Name is required";
  }
  if (!form.api_base.trim()) {
    errors.api_base = "API base is required";
  } else {
    const apiBaseError = validateHTTPURL(form.api_base);
    if (apiBaseError) {
      errors.api_base = apiBaseError;
    }
  }
  if (!form.api_type.trim()) {
    errors.api_type = "API type is required";
  }
  const endpointsError = validateRoutingEndpointsText(form.endpoints_text);
  if (endpointsError) {
    errors.endpoints_text = endpointsError;
  }
  return errors;
}

export function validateNodeForm(form: NodeForm) {
  const errors: Partial<Record<keyof NodeForm, string>> = {};
  if (!form.name.trim()) {
    errors.name = "Name is required";
  }
  if (!form.provider.trim() && !form.api_base.trim()) {
    errors.api_base = "Enter an API base or choose a provider";
  }
  if (!form.model.trim()) {
    errors.model = "Model name is required";
  }
  if (form.api_base.trim()) {
    const apiBaseError = validateHTTPURL(form.api_base);
    if (apiBaseError) {
      errors.api_base = apiBaseError;
    }
  }
  const endpointsError = validateRoutingEndpointsText(form.endpoints_text);
  if (endpointsError) {
    errors.endpoints_text = endpointsError;
  }
  return errors;
}

export function validateClientModelForm(form: ClientModelForm) {
  const errors: Partial<Record<keyof ClientModelForm, string>> = {};
  if (!form.name.trim()) {
    errors.name = "Name is required";
  }
  if (form.nodes.length === 0) {
    errors.nodes = "Choose at least one node";
  }
  return errors;
}

export function validateClientModelEditForm(form: ClientModelEditForm, nodes: string[], weightsResult: ParsedNodeWeights) {
  const errors: Partial<Record<keyof ClientModelEditForm, string>> = {};
  if (!form.name.trim()) {
    errors.name = "Name is required";
  }
  if (nodes.length === 0) {
    errors.nodes_text = "Enter at least one node";
  }
  if (weightsResult.error) {
    errors.node_weights_text = weightsResult.error;
  }
  return errors;
}

export function validateHTTPURL(value: string) {
  try {
    const url = new URL(value);
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      return "URL must be http or https";
    }
  } catch {
    return "URL must be absolute";
  }
  return "";
}

export function validateRoutingEndpointsText(value: string) {
  const endpoints = parseStringList(value);
  for (const endpoint of endpoints) {
    if (!validRoutingEndpoint(endpoint)) {
      return "Use chat, embeddings, rerank, messages, responses, all, or a /v1 path";
    }
  }
  return "";
}

export function validRoutingEndpoint(value: string) {
  const normalized = normalizeRoutingEndpoint(value);
  if (!normalized) return true;
  return ["chat", "embeddings", "rerank", "messages", "responses"].includes(normalized);
}

export function normalizeRoutingEndpoint(value: string) {
  let normalized = value.trim().toLowerCase();
  normalized = normalized.replace(/^\/+/, "");
  normalized = normalized.replace(/^v[12]\//, "");
  normalized = normalized.replace(/\/+$/, "");
  normalized = normalized.replace(/-/g, "_");
  switch (normalized) {
    case "":
    case "all":
    case "*":
      return "";
    case "chat":
    case "chat_completions":
    case "chat/completions":
    case "completions":
      return "chat";
    case "embedding":
    case "embeddings":
      return "embeddings";
    case "rerank":
    case "rankings":
      return "rerank";
    case "message":
    case "messages":
    case "anthropic":
    case "anthropic_messages":
      return "messages";
    case "response":
    case "responses":
      return "responses";
    default:
      return normalized;
  }
}

export function changedStringFields<T extends JsonRecord>(original: T, form: Record<string, string>, fields: string[]) {
  const payload: JsonRecord = {};
  fields.forEach((field) => {
    const nextValue = form[field].trim();
    const originalValue = typeof original[field] === "string" ? String(original[field]) : "";
    if (nextValue !== originalValue) {
      payload[field] = nextValue;
    }
  });
  return payload;
}

export function nextFallbackRowID() {
  return `fallback-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function fallbackRowsFromPayload(value: unknown): FallbackRow[] {
  return asArray<unknown>(value).flatMap((entry, index) => {
    if (!isJsonRecord(entry)) return [];
    return Object.entries(entry).flatMap(([rawSource, rawTargets], sourceIndex) => {
      const source = rawSource.trim();
      if (!source) return [];
      const targets = uniqueStrings(asArray<unknown>(rawTargets).filter(isNonEmptyString));
      return [{ id: `fallback-${index}-${sourceIndex}-${source}`, source, targets }];
    });
  });
}

export function fallbackPayloadFromRows(rows: FallbackRow[]) {
  return rows.map((row) => ({ [row.source.trim()]: uniqueStrings(row.targets) }));
}

export function validateFallbackRows(rows: FallbackRow[]) {
  const seen = new Set<string>();
  for (const row of rows) {
    const source = row.source.trim();
    if (!source) return "Every fallback chain needs a source";
    if (seen.has(source)) return `Fallback source "${source}" is duplicated`;
    seen.add(source);
    const targets = uniqueStrings(row.targets);
    if (targets.length === 0) return `Fallback source "${source}" needs at least one target`;
    if (targets.includes(source)) return `Fallback source "${source}" cannot target itself`;
  }
  return "";
}

export function fallbackNameOptions(legacyModels: string[], clientModels: ClientModelRecord[], nodes: NodeRecord[], rows: FallbackRow[]) {
  return uniqueStrings([
    ...legacyModels,
    ...clientModels.map((model) => resourceName(model)),
    ...nodes.map((node) => resourceName(node)),
    ...rows.flatMap((row) => [row.source, ...row.targets])
  ]).sort((left, right) => left.localeCompare(right));
}

export function normalizeModelTreePayload(payload: ModelTreePayload): ModelTreePayload {
  const nodes: Record<string, ModelTreeNode> = {};
  if (isJsonRecord(payload.nodes)) {
    Object.entries(payload.nodes).forEach(([name, rawNode]) => {
      if (!isJsonRecord(rawNode)) return;
      nodes[name] = {
        config: isJsonRecord(rawNode.config) ? rawNode.config : {},
        parent: typeof rawNode.parent === "string" ? rawNode.parent : "",
        children: asArray<unknown>(rawNode.children).filter(isNonEmptyString),
        protected: Boolean(rawNode.protected),
        editable: Boolean(rawNode.editable)
      };
    });
  }
  return {
    roots: asArray<unknown>(payload.roots).filter(isNonEmptyString),
    nodes
  };
}

export function modelTreeNames(tree: ModelTreePayload) {
  return Object.keys(tree.nodes || {}).sort((left, right) => left.localeCompare(right));
}

export function adminModelPathName(name: string) {
  // The Go handler calls PathUnescape on r.URL.Path, which net/http has already unescaped once.
  return encodeURIComponent(encodeURIComponent(name));
}

export function modelTreeRows(tree: ModelTreePayload): JsonRecord[] {
  return modelTreeNames(tree).map((name) => {
    const node = tree.nodes?.[name] || {};
    return {
      id: name,
      model: name,
      parent: node.parent || "",
      children: node.children || [],
      protected: Boolean(node.protected),
      editable: Boolean(node.editable)
    };
  });
}

export function renderModelTreeCell(
  row: JsonRecord,
  column: string,
  disabled: boolean,
  onCopy: (name?: string) => void,
  onDelete: (name?: string) => Promise<void>
) {
  const name = typeof row.model === "string" ? row.model : "";
  if (column === "model") return <span className="resource-name">{formatValue(row.model)}</span>;
  if (column === "children") return renderStringBadges(asArray<string>(row.children));
  if (column === "protected" || column === "editable") return renderBooleanBadge(row[column]);
  if (column === "actions") return <ModelTreeActions name={name} disabled={disabled} onCopy={onCopy} onDelete={onDelete} />;
  return formatValue(row[column]);
}

export function nextCopyModelName(source: string, existingNames: string[]) {
  const existing = new Set(existingNames);
  const base = `${source}-copy`;
  if (!existing.has(base)) return base;
  for (let index = 2; index < 1000; index += 1) {
    const candidate = `${base}-${index}`;
    if (!existing.has(candidate)) return candidate;
  }
  return `${base}-${Date.now()}`;
}

export function legacyModelDependentsConflict(err: unknown) {
  if (!(err instanceof APIError) || err.status !== 409) return null;
  const payload = apiErrorPayload(err);
  if (payloadErrorCode(payload) !== "model_has_dependents") return null;
  return {
    directChildren: modelDependentsList(payload, "direct_children"),
    descendants: modelDependentsList(payload, "dependents")
  };
}

export function modelDependentsList(payload: unknown, field: "direct_children" | "all_descendants" | "dependents") {
  if (!isJsonRecord(payload)) return [];
  let values = asArray<unknown>(payload[field]).filter(isNonEmptyString);
  if (values.length === 0 && isJsonRecord(payload.error)) {
    values = asArray<unknown>(payload.error[field]).filter(isNonEmptyString);
  }
  return uniqueStrings(values);
}

export function protectedModelNames(tree: ModelTreePayload, names: string[]) {
  return uniqueStrings(names).filter((name) => Boolean(tree.nodes?.[name]?.protected));
}

export function protectedModelPasswordMessage(names: string[]) {
  const label = names.length === 1 ? "model" : "models";
  return `Admin password required for protected ${label}: ${names.join(", ")}`;
}

export function legacyModelDeleteConfirmMessage(name: string, directChildren: string[], descendants: string[], protectedNames: string[], cascade: boolean) {
  const lines = [
    cascade ? `Delete "${name}" and ${descendantCountLabel(descendants.length)}?` : `Delete "${name}"?`,
    `Direct children: ${formatModelList(directChildren)}`,
    `All descendants: ${formatModelList(descendants)}`
  ];
  if (protectedNames.length > 0) {
    lines.push(`Protected: ${formatModelList(protectedNames)}`);
  }
  return lines.join("\n");
}

export function descendantCountLabel(count: number) {
  return `${count} descendant${count === 1 ? "" : "s"}`;
}

export function formatModelList(values: string[]) {
  return values.length > 0 ? values.join(", ") : "none";
}

export function parseNodeList(value: string) {
  return parseStringList(value);
}

export function parseNodeWeights(value: string): ParsedNodeWeights {
  const weights: Record<string, number> = {};
  const seen = new Set<string>();
  const lines = value.split("\n");
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].trim();
    if (!line) continue;
    const match = line.match(/^([^:=]+)\s*[:=]\s*(\d+)$/);
    if (!match) {
      return { weights, error: `Line ${index + 1} must be node=weight` };
    }
    const nodeName = match[1].trim();
    const weight = Number(match[2]);
    if (!nodeName) {
      return { weights, error: `Line ${index + 1} needs a node name` };
    }
    if (!Number.isSafeInteger(weight) || weight < 0) {
      return { weights, error: `Line ${index + 1} weight must be a non-negative integer` };
    }
    if (seen.has(nodeName)) {
      return { weights, error: `Duplicate weight for "${nodeName}"` };
    }
    seen.add(nodeName);
    weights[nodeName] = weight;
  }
  return { weights };
}

export function formatNodeWeights(value: unknown) {
  const weights = asIntegerRecord(value);
  return Object.keys(weights)
    .sort()
    .map((key) => `${key}=${weights[key]}`)
    .join("\n");
}

export function asIntegerRecord(value: unknown) {
  if (!isJsonRecord(value)) return {};
  const out: Record<string, number> = {};
  Object.entries(value).forEach(([key, rawValue]) => {
    if (typeof rawValue === "number" && Number.isSafeInteger(rawValue)) {
      out[key] = rawValue;
    }
  });
  return out;
}

export function integerRecordsEqual(left: Record<string, number>, right: Record<string, number>) {
  const leftKeys = Object.keys(left).sort();
  const rightKeys = Object.keys(right).sort();
  if (!arraysEqual(leftKeys, rightKeys)) return false;
  return leftKeys.every((key) => left[key] === right[key]);
}

export function renderProviderCell(
  row: JsonRecord,
  column: string,
  disabled: boolean,
  onEdit: (provider: ProviderRecord) => void,
  onDelete: (name?: string) => Promise<void>
) {
  const name = resourceName(row);
  if (column === "name") return <span className="resource-name">{name || "-"}</span>;
  if (column === "api_type") return row.api_type ? <Badge>{formatValue(row.api_type)}</Badge> : "-";
  if (column === "models") return formatProviderModels(row.models) || "Shared settings only";
  if (column === "endpoints") return renderStringBadges(asArray<string>(row.endpoints));
  if (column === "actions") return <RoutingActions row={row as ProviderRecord} name={name} disabled={disabled} onEdit={onEdit} onDelete={onDelete} />;
  return formatValue(row[column]);
}

export function renderNodeCell(
  row: JsonRecord,
  column: string,
  disabled: boolean,
  onEdit: (node: NodeRecord) => void,
  onDelete: (name?: string) => Promise<void>
) {
  const name = resourceName(row);
  if (column === "name") return <span className="resource-name">{name || "-"}</span>;
  if (column === "provider") return row.provider ? <Badge>{formatValue(row.provider)}</Badge> : "-";
  if (column === "endpoints") return renderStringBadges(asArray<string>(row.endpoints));
  if (column === "actions") return <RoutingActions row={row as NodeRecord} name={name} disabled={disabled} onEdit={onEdit} onDelete={onDelete} />;
  return formatValue(row[column]);
}

export function renderClientModelCell(
  row: JsonRecord,
  column: string,
  disabled: boolean,
  onEdit: (clientModel: ClientModelRecord) => void,
  onDelete: (name?: string) => Promise<void>
) {
  const name = resourceName(row);
  if (column === "name") return <span className="resource-name">{name || "-"}</span>;
  if (column === "nodes") {
    return renderStringBadges(asArray<string>(row.nodes));
  }
  if (column === "node_weights") {
    const weights = asIntegerRecord(row.node_weights);
    const entries = Object.keys(weights)
      .sort()
      .map((key) => `${key}=${weights[key]}`);
    return renderStringBadges(entries);
  }
  if (column === "actions") return <RoutingActions row={row as ClientModelRecord} name={name} disabled={disabled} onEdit={onEdit} onDelete={onDelete} />;
  return formatValue(row[column]);
}

export function renderStringBadges(values: string[]) {
  if (values.length === 0) return "-";
  return (
    <span className="badge-stack">
      {values.map((value, index) => (
        <Badge key={`${value}-${index}`}>{value}</Badge>
      ))}
    </span>
  );
}

export function resourceName(row: JsonRecord) {
  if (typeof row.name === "string") return row.name;
  if (typeof row.model_name === "string") return row.model_name;
  return "";
}
