export type JsonRecord = Record<string, unknown>;

export type TabID = "overview" | "observability" | "traces" | "catalog" | "logs" | "config" | "models" | "routing" | "keys";

export type AdminPasswordStatusTone = "neutral" | "good" | "warn" | "bad";

export type AdminPasswordFeedback = { text: string; tone: AdminPasswordStatusTone };

export type LogRecord = {
  id?: string;
  request_time?: string;
  created_at?: string;
  model_name?: string;
  path?: string;
  request_path?: string;
  outcome?: string;
  duration_ms?: number;
  backend_name?: string;
  backend_status?: number;
  total_tokens?: number;
  warning_count?: number;
  app_key_id?: string;
  trace_id?: string;
  traceId?: string;
  traceID?: string;
  stop_reason?: string;
  is_stream?: boolean;
  is_tool_call?: boolean;
  method?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  reasoning_tokens?: number;
  usage_stats?: JsonRecord;
};

export type LogsPayload = {
  logs?: LogRecord[];
  data?: LogRecord[];
  total?: number;
  limit?: number;
  offset?: number;
  has_more?: boolean;
};

export type LogFilters = {
  model: string;
  outcome: string;
  stopReason: string;
  toolCall: string;
  startDate: string;
  endDate: string;
  search: string;
};

export type LogMeta = {
  total: number;
  limit: number;
  offset: number;
  hasMore: boolean;
};

export type ModelRecord = {
  id?: string;
  object?: string;
  supports_reasoning?: boolean;
  capabilities?: JsonRecord;
  tags?: string[];
};

export type ModelsPayload = {
  data?: ModelRecord[];
};

export type ProviderRecord = {
  name?: string;
  api_base?: string;
  api_type?: string;
  [key: string]: unknown;
};

export type NodeRecord = {
  name?: string;
  provider?: string;
  model?: string;
  api_base?: string;
  [key: string]: unknown;
};

export type ClientModelRecord = {
  name?: string;
  model_name?: string;
  nodes?: string[];
  [key: string]: unknown;
};

export type RouterSettingsPayload = {
  fallbacks?: unknown;
};

export type FallbackRow = {
  id: string;
  source: string;
  targets: string[];
};

export type FallbackForm = {
  source: string;
  targets: string[];
};

export type CopyModelForm = {
  source: string;
  target: string;
  error: string;
};

export type ModelTreeNode = {
  config?: JsonRecord;
  parent?: string;
  children?: string[];
  protected?: boolean;
  editable?: boolean;
};

export type ModelTreePayload = {
  roots?: string[];
  nodes?: Record<string, ModelTreeNode>;
};

export type ModelAncestryPayload = {
  model?: string;
  chain?: string[];
  inheritance_depth?: number;
};

export type ModelDependentsPayload = {
  model?: string;
  direct_children?: string[];
  all_descendants?: string[];
  descendant_count?: number;
};

export type RoutingPayloads = {
  providers?: ProviderRecord[];
  nodes?: NodeRecord[];
  client_models?: ClientModelRecord[];
};

export type ProviderForm = {
  models_text: string;
  name: string;
  api_base: string;
  api_type: string;
  api_key: string;
  endpoints_text: string;
  limits_text: string;
};

export type NodeForm = {
  upstream_model: string;
  api_key: string;
  name: string;
  provider: string;
  model: string;
  api_base: string;
  endpoints_text: string;
  limits_text: string;
  modules_text: string;
  allowed_keys_text: string;
};

export type ClientModelForm = {
  name: string;
  nodes: string[];
};

export type ClientModelEditForm = {
  name: string;
  nodes_text: string;
  node_weights_text: string;
  balancing: string;
  limits_text: string;
  allowed_keys_text: string;
};

export type RoutingEditState =
  | {
      kind: "provider";
      original: ProviderRecord;
      form: ProviderForm;
      errors: Partial<Record<keyof ProviderForm, string>>;
    }
  | {
      kind: "node";
      original: NodeRecord;
      form: NodeForm;
      errors: Partial<Record<keyof NodeForm, string>>;
    }
  | {
      kind: "client-model";
      original: ClientModelRecord;
      form: ClientModelEditForm;
      errors: Partial<Record<keyof ClientModelEditForm, string>>;
    };

export type LimitsRecord = JsonRecord;

export type AllowedKeysValue = string | string[];

export type AppKeyRecord = {
  key_id?: string;
  group_id?: string;
  name?: string;
  description?: string;
  enabled?: boolean;
  models?: string[];
  concurrency_limit?: number;
  priority?: number;
  queue_timeout?: number;
  limits?: LimitsRecord;
  required_headers?: JsonRecord[];
  has_secret?: boolean;
  [key: string]: unknown;
};

export type AppKeysPayload = {
  status?: string;
  enabled?: boolean;
  header_name?: string;
  allow_unauthenticated?: boolean;
  required_headers?: JsonRecord[];
  defaults?: LimitsRecord;
  unauthenticated?: LimitsRecord;
  groups?: JsonRecord[];
  keys?: AppKeyRecord[];
};

export type AppKeyMutationPayload = {
  status?: string;
  key_id?: string;
  secret?: string;
  message?: string;
  key?: AppKeyRecord;
};

export type AppKeyForm = {
  key_id: string;
  group_id: string;
  name: string;
  description: string;
  enabled: boolean;
  models_text: string;
  concurrency_limit: string;
  priority: string;
  queue_timeout: string;
  limits_text: string;
  required_headers_text: string;
};

export type AppKeyConfigForm = {
  enabled: boolean;
  header_name: string;
  allow_unauthenticated: boolean;
  defaults_text: string;
  unauthenticated_text: string;
  groups_text: string;
  required_headers_text: string;
};

export type AppKeyEditState = {
  original: AppKeyRecord;
  form: AppKeyForm;
  errors: Partial<Record<keyof AppKeyForm, string>>;
};

export type SecretReveal = {
  keyId: string;
  secret: string;
  message: string;
};

export type ModelForm = {
  model_name: string;
  api_base: string;
  model: string;
  api_key: string;
  api_type: string;
  fallbacks: string;
  mode: string;
  scope: string;
  auth_url: string;
  client_id: string;
  client_secret: string;
  client_cert: string;
  client_key: string;
  verify_ssl: boolean;
  emulate_tool_calls: boolean;
};

export type ValidationIssue = {
  path: string;
  severity: "error" | "warning";
  code: string;
  message: string;
};

export type ValidationPayload = {
  ok: boolean;
  issues: ValidationIssue[];
  error_count: number;
  warning_count: number;
};

export type CacheStats = {
  enabled?: boolean;
  mode?: string;
  hits?: number;
  misses?: number;
  writes?: number;
  evictions?: number;
  entries?: number;
  total_entry_bytes?: number;
  max_entries?: number;
  max_entry_bytes?: number;
  trace_id?: string;
  traceId?: string;
  traceID?: string;
  semantic?: SemanticCacheStats;
};

export type SemanticCacheStats = {
  enabled?: boolean;
  mode?: string;
  hits?: number;
  misses?: number;
  writes?: number;
  evictions?: number;
  embedding_errors?: number;
  threshold_rejected?: number;
  entries?: number;
  total_entry_bytes?: number;
  max_entries?: number;
  max_entry_bytes?: number;
  top_k?: number;
  similarity_threshold?: number;
};

export type CatalogNodeEntry = {
  node?: string;
  status?: string;
  source?: string;
  last_sync?: string;
  next_sync?: string;
  error_class?: string;
  configured_model?: string;
  target_model?: string;
};

export type CatalogProviderEntry = {
  provider?: string;
  nodes?: CatalogNodeEntry[];
};

export type CatalogModelEntry = {
  model?: string;
  providers?: CatalogProviderEntry[];
};

export type CatalogWarning = {
  severity?: "info" | "warning" | string;
  code?: string;
  message?: string;
  model?: string;
  provider?: string;
  node?: string;
  error_class?: string;
};

export type CatalogSnapshot = {
  enabled?: boolean;
  probe_enabled?: boolean;
  generated_at?: string;
  models?: CatalogModelEntry[];
  warnings?: CatalogWarning[];
};

export type CatalogRow = JsonRecord & {
  model: string;
  provider: string;
  node: string;
  status: string;
  source: string;
  error_class: string;
  configured_model: string;
  target_model: string;
  last_sync?: string;
  next_sync?: string;
};

export type BadgeTone = "neutral" | "good" | "warn" | "bad";

export type TraceRequestRecord = LogRecord & {
  method?: string;
  model?: string;
  status?: number;
  span_id?: string;
  duration_seconds?: number;
  error_count?: number;
  attempt_count?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  reasoning_tokens?: number;
  key_scope?: string;
  is_tool_call?: boolean;
};

export type TraceAttemptRecord = {
  request_id?: string;
  trace_id?: string;
  attempt_index?: number;
  backend_name?: string;
  retry_index?: number;
  status?: number;
  retryable?: boolean;
  outcome?: string;
  duration_ms?: number;
  error_type?: string;
};

export type TraceErrorRecord = {
  id?: string;
  timestamp?: string;
  created_at?: string;
  error_type?: string;
  request_log_id?: string;
  model_name?: string;
  backend_name?: string;
  http_status?: number;
};

export type TraceDetailPayload = {
  trace_id?: string;
  requests?: TraceRequestRecord[];
  logs?: TraceRequestRecord[];
  data?: TraceRequestRecord[];
  attempts?: TraceAttemptRecord[];
  attempt_trail?: TraceAttemptRecord[];
  errors?: TraceErrorRecord[];
  error_logs?: TraceErrorRecord[];
  total?: number;
  request_count?: number;
  returned?: number;
  limit?: number;
  offset?: number;
  has_more?: boolean;
  started_at?: string;
  ended_at?: string;
  duration_ms?: number;
  models?: string[];
  backends?: string[];
  outcomes?: string[];
};

export type TraceTimelineField = {
  label: string;
  value: unknown;
  tone?: BadgeTone;
};

export type TraceTimelineItem = {
  id: string;
  phase: string;
  title: string;
  tone: BadgeTone;
  time?: string;
  fields: TraceTimelineField[];
  logID?: string;
};

export type LogDetailSection = {
  id: string;
  title: string;
  value: unknown;
  defaultOpen?: boolean;
  tone?: BadgeTone;
};

export type SelectedProviderModel = { name: string; upstream_model?: string };

export type ParsedNodeWeights = {
  weights: Record<string, number>;
  error?: string;
};
