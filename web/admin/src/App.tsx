import { Activity, BarChart3, CheckCircle2, Clock3, Copy, Database, FileJson, Gauge, GitBranch, HardDrive, HeartPulse, KeyRound, ListFilter, Network, Pencil, Plus, RefreshCw, Save, Search, Server, Shield, Trash2, UploadCloud, X } from "lucide-react";
import { FormEvent, useEffect, useMemo, useState } from "react";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";

type JsonRecord = Record<string, unknown>;
type TabID = "overview" | "observability" | "traces" | "catalog" | "logs" | "config" | "models" | "routing" | "keys";

type LogRecord = {
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
};

type LogsPayload = {
  logs?: LogRecord[];
  data?: LogRecord[];
  total?: number;
  limit?: number;
  offset?: number;
  has_more?: boolean;
};

type ModelRecord = {
  id?: string;
  object?: string;
  supports_reasoning?: boolean;
  capabilities?: JsonRecord;
  tags?: string[];
};

type ModelsPayload = {
  data?: ModelRecord[];
};

type ProviderRecord = {
  name?: string;
  api_base?: string;
  api_type?: string;
  [key: string]: unknown;
};

type NodeRecord = {
  name?: string;
  provider?: string;
  model?: string;
  api_base?: string;
  [key: string]: unknown;
};

type ClientModelRecord = {
  name?: string;
  model_name?: string;
  nodes?: string[];
  [key: string]: unknown;
};

type RouterSettingsPayload = {
  fallbacks?: unknown;
};

type FallbackRow = {
  id: string;
  source: string;
  targets: string[];
};

type FallbackForm = {
  source: string;
  targets: string[];
};

type CopyModelForm = {
  source: string;
  target: string;
  error: string;
};

type ModelTreeNode = {
  config?: JsonRecord;
  parent?: string;
  children?: string[];
  protected?: boolean;
  editable?: boolean;
};

type ModelTreePayload = {
  roots?: string[];
  nodes?: Record<string, ModelTreeNode>;
};

type ModelAncestryPayload = {
  model?: string;
  chain?: string[];
  inheritance_depth?: number;
};

type ModelDependentsPayload = {
  model?: string;
  direct_children?: string[];
  all_descendants?: string[];
  descendant_count?: number;
};

type RoutingPayloads = {
  providers?: ProviderRecord[];
  nodes?: NodeRecord[];
  client_models?: ClientModelRecord[];
};

type ProviderForm = {
  name: string;
  api_base: string;
  api_type: string;
  api_key: string;
  endpoints_text: string;
  limits_text: string;
};

type NodeForm = {
  name: string;
  provider: string;
  model: string;
  api_base: string;
  endpoints_text: string;
  limits_text: string;
  modules_text: string;
  allowed_keys_text: string;
};

type ClientModelForm = {
  name: string;
  nodes: string[];
};

type ClientModelEditForm = {
  name: string;
  nodes_text: string;
  node_weights_text: string;
  balancing: string;
  limits_text: string;
  allowed_keys_text: string;
};

type RoutingEditState =
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

type LimitsRecord = JsonRecord;
type AllowedKeysValue = string | string[];

type AppKeyRecord = {
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

type AppKeysPayload = {
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

type AppKeyMutationPayload = {
  status?: string;
  key_id?: string;
  secret?: string;
  message?: string;
  key?: AppKeyRecord;
};

type AppKeyForm = {
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

type AppKeyConfigForm = {
  enabled: boolean;
  header_name: string;
  allow_unauthenticated: boolean;
  defaults_text: string;
  unauthenticated_text: string;
  groups_text: string;
  required_headers_text: string;
};

type AppKeyEditState = {
  original: AppKeyRecord;
  form: AppKeyForm;
  errors: Partial<Record<keyof AppKeyForm, string>>;
};

type SecretReveal = {
  keyId: string;
  secret: string;
  message: string;
};

type ModelForm = {
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

type ValidationIssue = {
  path: string;
  severity: "error" | "warning";
  code: string;
  message: string;
};

type ValidationPayload = {
  ok: boolean;
  issues: ValidationIssue[];
  error_count: number;
  warning_count: number;
};

type CacheStats = {
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

type SemanticCacheStats = {
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

type CatalogNodeEntry = {
  node?: string;
  status?: string;
  source?: string;
  last_sync?: string;
  next_sync?: string;
  error_class?: string;
  configured_model?: string;
  target_model?: string;
};

type CatalogProviderEntry = {
  provider?: string;
  nodes?: CatalogNodeEntry[];
};

type CatalogModelEntry = {
  model?: string;
  providers?: CatalogProviderEntry[];
};

type CatalogWarning = {
  severity?: "info" | "warning" | string;
  code?: string;
  message?: string;
  model?: string;
  provider?: string;
  node?: string;
  error_class?: string;
};

type CatalogSnapshot = {
  enabled?: boolean;
  probe_enabled?: boolean;
  generated_at?: string;
  models?: CatalogModelEntry[];
  warnings?: CatalogWarning[];
};

type CatalogRow = JsonRecord & {
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

type BadgeTone = "neutral" | "good" | "warn" | "bad";

type TraceRequestRecord = LogRecord & {
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

type TraceAttemptRecord = {
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

type TraceErrorRecord = {
  id?: string;
  timestamp?: string;
  created_at?: string;
  error_type?: string;
  request_log_id?: string;
  model_name?: string;
  backend_name?: string;
  http_status?: number;
};

type TraceDetailPayload = {
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

type TraceTimelineField = {
  label: string;
  value: unknown;
  tone?: BadgeTone;
};

type TraceTimelineItem = {
  id: string;
  phase: string;
  title: string;
  tone: BadgeTone;
  time?: string;
  fields: TraceTimelineField[];
};

const tabs: Array<{ id: TabID; label: string; icon: typeof Activity }> = [
  { id: "overview", label: "Overview", icon: Activity },
  { id: "observability", label: "Observability", icon: BarChart3 },
  { id: "traces", label: "Traces", icon: GitBranch },
  { id: "catalog", label: "Catalog", icon: ListFilter },
  { id: "logs", label: "Logs", icon: Database },
  { id: "config", label: "Config", icon: FileJson },
  { id: "models", label: "Models", icon: Server },
  { id: "routing", label: "Routing", icon: Network },
  { id: "keys", label: "App Keys", icon: KeyRound }
];

const emptyAppKeyForm: AppKeyForm = {
  key_id: "",
  group_id: "",
  name: "",
  description: "",
  enabled: true,
  models_text: "",
  concurrency_limit: "",
  priority: "",
  queue_timeout: "",
  limits_text: "{}",
  required_headers_text: "[]"
};

const emptyAppKeyConfigForm: AppKeyConfigForm = {
  enabled: false,
  header_name: "x-app-key",
  allow_unauthenticated: false,
  defaults_text: "{}",
  unauthenticated_text: "{}",
  groups_text: "[]",
  required_headers_text: "[]"
};

const emptyCopyModelForm: CopyModelForm = {
  source: "",
  target: "",
  error: ""
};

export function AdminApp() {
  const [activeTab, setActiveTab] = useState<TabID>(() => tabFromLocation(window.location));
  const [adminPassword, setAdminPassword] = useState(() => localStorage.getItem("yallmp.adminPassword") || "");
  const [loading, setLoading] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [health, setHealth] = useState<JsonRecord[]>([]);
  const [usage, setUsage] = useState<JsonRecord>({});
  const [cacheStats, setCacheStats] = useState<CacheStats>({});
  const [cacheError, setCacheError] = useState("");
  const [cacheStatus, setCacheStatus] = useState<number | null>(null);
  const [observabilityLoading, setObservabilityLoading] = useState(false);
  const [observabilityTraceID, setObservabilityTraceID] = useState("");
  const [catalog, setCatalog] = useState<CatalogSnapshot | null>(null);
  const [catalogError, setCatalogError] = useState("");
  const [catalogStatus, setCatalogStatus] = useState<number | null>(null);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [models, setModels] = useState<ModelRecord[]>([]);
  const [modelStats, setModelStats] = useState<JsonRecord[]>([]);
  const [stopReasons, setStopReasons] = useState<JsonRecord[]>([]);
  const [toolRate, setToolRate] = useState<JsonRecord>({});
  const [logs, setLogs] = useState<LogRecord[]>([]);
  const [logMeta, setLogMeta] = useState({ total: 0, hasMore: false });
  const [selectedLog, setSelectedLog] = useState<JsonRecord | null>(null);
  const [filters, setFilters] = useState({ model: "", outcome: "", search: "" });
  const [traceInput, setTraceInput] = useState(() => traceIDFromLocation(window.location));
  const [traceDetail, setTraceDetail] = useState<TraceDetailPayload | null>(null);
  const [traceLoading, setTraceLoading] = useState(false);
  const [traceError, setTraceError] = useState("");
  const [traceStatus, setTraceStatus] = useState<number | null>(null);
  const [configText, setConfigText] = useState("");
  const [configIssues, setConfigIssues] = useState<ValidationIssue[]>([]);
  const [validationSummary, setValidationSummary] = useState({ errors: 0, warnings: 0 });
  const [modelForm, setModelForm] = useState<ModelForm>({
    model_name: "",
    api_base: "",
    model: "",
    api_key: "",
    api_type: "openai",
    fallbacks: "",
    mode: "",
    scope: "",
    auth_url: "",
    client_id: "",
    client_secret: "",
    client_cert: "",
    client_key: "",
    verify_ssl: true,
    emulate_tool_calls: false
  });
  const [modelErrors, setModelErrors] = useState<Partial<Record<keyof ModelForm, string>>>({});
  const [providers, setProviders] = useState<ProviderRecord[]>([]);
  const [nodes, setNodes] = useState<NodeRecord[]>([]);
  const [clientModels, setClientModels] = useState<ClientModelRecord[]>([]);
  const [routerFallbacks, setRouterFallbacks] = useState<FallbackRow[]>([]);
  const [newFallback, setNewFallback] = useState<FallbackForm>({ source: "", targets: [] });
  const [fallbackError, setFallbackError] = useState("");
  const [modelTree, setModelTree] = useState<ModelTreePayload>({ roots: [], nodes: {} });
  const [copyModelForm, setCopyModelForm] = useState<CopyModelForm>(emptyCopyModelForm);
  const [dependencyModel, setDependencyModel] = useState("");
  const [modelAncestry, setModelAncestry] = useState<ModelAncestryPayload | null>(null);
  const [modelDependents, setModelDependents] = useState<ModelDependentsPayload | null>(null);
  const [dependencyError, setDependencyError] = useState("");
  const [providerForm, setProviderForm] = useState<ProviderForm>({
    name: "",
    api_base: "",
    api_type: "openai",
    api_key: "",
    endpoints_text: "",
    limits_text: "{}"
  });
  const [nodeForm, setNodeForm] = useState<NodeForm>({
    name: "",
    provider: "",
    model: "",
    api_base: "",
    endpoints_text: "",
    limits_text: "{}",
    modules_text: "{}",
    allowed_keys_text: "[]"
  });
  const [clientModelForm, setClientModelForm] = useState<ClientModelForm>({
    name: "",
    nodes: []
  });
  const [providerErrors, setProviderErrors] = useState<Partial<Record<keyof ProviderForm, string>>>({});
  const [nodeErrors, setNodeErrors] = useState<Partial<Record<keyof NodeForm, string>>>({});
  const [clientModelErrors, setClientModelErrors] = useState<Partial<Record<keyof ClientModelForm, string>>>({});
  const [routingConflict, setRoutingConflict] = useState("");
  const [routingEdit, setRoutingEdit] = useState<RoutingEditState | null>(null);
  const [appKeys, setAppKeys] = useState<AppKeyRecord[]>([]);
  const [appKeysConfig, setAppKeysConfig] = useState<AppKeysPayload>({});
  const [appKeyForm, setAppKeyForm] = useState<AppKeyForm>(emptyAppKeyForm);
  const [appKeyErrors, setAppKeyErrors] = useState<Partial<Record<keyof AppKeyForm, string>>>({});
  const [appKeyEdit, setAppKeyEdit] = useState<AppKeyEditState | null>(null);
  const [appKeyConfigForm, setAppKeyConfigForm] = useState<AppKeyConfigForm>(emptyAppKeyConfigForm);
  const [appKeyConfigErrors, setAppKeyConfigErrors] = useState<Partial<Record<keyof AppKeyConfigForm, string>>>({});
  const [secretReveal, setSecretReveal] = useState<SecretReveal | null>(null);

  const healthyCount = useMemo(() => health.filter((item) => item.healthy !== false).length, [health]);
  const quotaRows = useMemo(() => quotaScopes(usage), [usage]);
  const cacheRows = useMemo(() => cacheSnapshotRows(cacheStats), [cacheStats]);
  const catalogRows = useMemo(() => catalogSnapshotRows(catalog), [catalog]);
  const catalogWarnings = useMemo(() => asArray<CatalogWarning>(catalog?.warnings), [catalog]);
  const traceRequests = useMemo(() => traceRequestsFromPayload(traceDetail), [traceDetail]);
  const traceAttempts = useMemo(() => traceAttemptsFromPayload(traceDetail), [traceDetail]);
  const traceErrors = useMemo(() => traceErrorsFromPayload(traceDetail), [traceDetail]);
  const traceTimeline = useMemo(() => traceTimelineItems(traceDetail), [traceDetail]);
  const logsHaveTraceIDs = useMemo(() => logs.some((row) => Boolean(traceIDFromRecord(row))), [logs]);
  const providerNames = useMemo(() => providers.map((provider) => provider.name).filter(isNonEmptyString), [providers]);
  const nodeNames = useMemo(() => nodes.map((node) => node.name).filter(isNonEmptyString), [nodes]);
  const legacyModelNames = useMemo(() => modelTreeNames(modelTree), [modelTree]);
  const fallbackModelNames = useMemo(() => fallbackNameOptions(legacyModelNames, clientModels, nodes, routerFallbacks), [legacyModelNames, clientModels, nodes, routerFallbacks]);
  const appKeyGroupIDs = useMemo(() => appKeyGroups(appKeysConfig).map((group) => String(group.group_id || "")).filter(isNonEmptyString), [appKeysConfig]);

  useEffect(() => {
    if (adminPassword) {
      localStorage.setItem("yallmp.adminPassword", adminPassword);
    } else {
      localStorage.removeItem("yallmp.adminPassword");
    }
  }, [adminPassword]);

  useEffect(() => {
    void refreshAll();
    if (activeTab === "traces" && traceInput.trim()) {
      void loadTraceDashboard(traceInput);
    }
  }, []);

  useEffect(() => {
    if (activeTab === "observability") {
      void loadObservability().catch((err) => setError(errorMessage(err)));
    }
    if (activeTab === "catalog") {
      void loadCatalog();
    }
    if (activeTab === "routing") {
      void loadRouting().catch((err) => setError(errorMessage(err)));
    }
    if (activeTab === "keys") {
      void loadAppKeys().catch((err) => setError(errorMessage(err)));
    }
  }, [activeTab]);

  useEffect(() => {
    if (activeTab === "routing" && dependencyModel) {
      void loadModelDependencies(dependencyModel).catch((err) => setDependencyError(errorMessage(err)));
    } else if (!dependencyModel) {
      setModelAncestry(null);
      setModelDependents(null);
    }
  }, [activeTab, dependencyModel, adminPassword]);

  useEffect(() => {
    const handlePopState = () => {
      const nextTab = tabFromLocation(window.location);
      const nextTraceID = traceIDFromLocation(window.location);
      setActiveTab(nextTab);
      setTraceInput(nextTraceID);
      if (nextTab === "traces" && nextTraceID.trim()) {
        void loadTraceDashboard(nextTraceID);
      }
    };
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, [adminPassword]);

  function switchTab(tab: TabID) {
    setActiveTab(tab);
    setSecretReveal(null);
    const nextPath = pathForTab(tab);
    if (`${window.location.pathname}${window.location.search}` !== nextPath) {
      window.history.pushState(null, "", nextPath);
    }
  }

  async function refreshAll() {
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      await Promise.all([loadOverview(), loadObservability(), loadCatalog(), loadLogs(), loadConfig(), loadRouting(), loadAppKeys()]);
      setNotice("Refreshed");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function loadOverview() {
    const [healthPayload, usagePayload, configuredModelsPayload, modelsPayload, stopsPayload, toolPayload] = await Promise.all([
      requestJSON<JsonRecord>("/v1/health", undefined, adminPassword),
      requestJSON<JsonRecord>("/v1/usage", undefined, adminPassword),
      requestJSON<ModelsPayload>("/v1/models", undefined, adminPassword).catch((): ModelsPayload => ({})),
      requestJSON<JsonRecord>("/admin/logs/models", undefined, adminPassword).catch((): JsonRecord => ({})),
      requestJSON<JsonRecord>("/admin/logs/stop-reasons", undefined, adminPassword).catch((): JsonRecord => ({})),
      requestJSON<JsonRecord>("/admin/logs/tool-call-rate", undefined, adminPassword).catch((): JsonRecord => ({}))
    ]);
    setHealth(asArray<JsonRecord>(healthPayload.data));
    setUsage(usagePayload);
    setModels(asArray<ModelRecord>(configuredModelsPayload.data));
    setModelStats(asArray<JsonRecord>(modelsPayload.models));
    setStopReasons(asArray<JsonRecord>(stopsPayload.stop_reasons));
    setToolRate(toolPayload);
  }

  async function loadObservability() {
    setObservabilityLoading(true);
    setCacheError("");
    setCacheStatus(null);
    try {
      const [cachePayload, healthPayload, usagePayload] = await Promise.all([
        requestJSON<CacheStats>("/admin/cache", undefined, adminPassword).catch((err): CacheStats => {
          setCacheError(errorMessage(err));
          setCacheStatus(err instanceof APIError ? err.status : null);
          return {};
        }),
        requestJSON<JsonRecord>("/v1/health").catch((err): JsonRecord => {
          setError(errorMessage(err));
          return {};
        }),
        requestJSON<JsonRecord>("/v1/usage").catch((err): JsonRecord => {
          setError(errorMessage(err));
          return {};
        })
      ]);
      const nextHealth = asArray<JsonRecord>(healthPayload.data);
      setCacheStats(cachePayload);
      setHealth(nextHealth);
      setUsage(usagePayload);
      setObservabilityTraceID(extractTraceID([cachePayload, healthPayload, usagePayload, ...nextHealth]));
    } finally {
      setObservabilityLoading(false);
    }
  }

  async function loadCatalog() {
    setCatalogLoading(true);
    setCatalogError("");
    setCatalogStatus(null);
    try {
      const payload = await requestJSON<CatalogSnapshot>("/admin/catalog", undefined, adminPassword);
      setCatalog(payload);
    } catch (err) {
      setCatalog(null);
      setCatalogError(catalogSafeErrorMessage(err));
      setCatalogStatus(err instanceof APIError ? err.status : null);
    } finally {
      setCatalogLoading(false);
    }
  }

  async function loadLogs() {
    const params = new URLSearchParams({ limit: "25" });
    if (filters.model) params.set("model", filters.model);
    if (filters.outcome) params.set("outcome", filters.outcome);
    if (filters.search) params.set("search", filters.search);
    const payload = await requestJSON<LogsPayload>(`/admin/logs?${params.toString()}`, undefined, adminPassword);
    const rows = payload.logs || payload.data || [];
    setLogs(rows);
    setLogMeta({ total: Number(payload.total || rows.length), hasMore: Boolean(payload.has_more) });
  }

  async function loadTraceDashboard(rawTraceID = traceInput) {
    const traceID = rawTraceID.trim();
    if (!traceID) {
      setTraceError("Enter a trace ID");
      setTraceStatus(null);
      setTraceDetail(null);
      return;
    }
    setTraceLoading(true);
    setTraceError("");
    setTraceStatus(null);
    try {
      const tracePayload = await requestJSON<TraceDetailPayload>(`/admin/traces/${encodeURIComponent(traceID)}`, undefined, adminPassword);
      setTraceDetail(tracePayload);
      setTraceInput(tracePayload.trace_id || traceID);
    } catch (err) {
      setTraceDetail(null);
      setTraceError(traceSafeErrorMessage(err));
      setTraceStatus(err instanceof APIError ? err.status : null);
    } finally {
      setTraceLoading(false);
    }
  }

  function submitTraceSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void openTraceDashboard(traceInput);
  }

  async function openTraceDashboard(rawTraceID: string) {
    const traceID = rawTraceID.trim();
    if (!traceID) {
      setTraceError("Enter a trace ID");
      return;
    }
    setActiveTab("traces");
    setSecretReveal(null);
    setTraceInput(traceID);
    const nextPath = pathForTrace(traceID);
    if (`${window.location.pathname}${window.location.search}` !== nextPath) {
      window.history.pushState(null, "", nextPath);
    }
    await loadTraceDashboard(traceID);
  }

  async function loadConfig() {
    const payload = await requestJSON<JsonRecord>("/admin/config", undefined, adminPassword);
    setConfigText(JSON.stringify(payload, null, 2));
    setConfigIssues([]);
    setValidationSummary({ errors: 0, warnings: 0 });
  }

  async function loadRouting() {
    const [providersPayload, nodesPayload, clientModelsPayload, routerSettingsPayload, treePayload] = await Promise.all([
      requestJSON<RoutingPayloads>("/admin/providers", undefined, adminPassword),
      requestJSON<RoutingPayloads>("/admin/nodes", undefined, adminPassword),
      requestJSON<RoutingPayloads>("/admin/client-models", undefined, adminPassword),
      requestJSON<RouterSettingsPayload>("/admin/router-settings", undefined, adminPassword),
      requestJSON<ModelTreePayload>("/admin/models/tree", undefined, adminPassword).catch((): ModelTreePayload => ({ roots: [], nodes: {} }))
    ]);
    setProviders(asArray<ProviderRecord>(providersPayload.providers));
    setNodes(asArray<NodeRecord>(nodesPayload.nodes));
    setClientModels(asArray<ClientModelRecord>(clientModelsPayload.client_models));
    setRouterFallbacks(fallbackRowsFromPayload(routerSettingsPayload.fallbacks));
    setFallbackError("");
    const normalizedTree = normalizeModelTreePayload(treePayload);
    setModelTree(normalizedTree);
    setDependencyModel((current) => current || modelTreeNames(normalizedTree)[0] || "");
  }

  async function loadAppKeys() {
    const payload = await requestJSON<AppKeysPayload>("/admin/keys", undefined, adminPassword);
    setAppKeys(asArray<AppKeyRecord>(payload.keys));
    setAppKeysConfig(payload);
    setAppKeyConfigForm(appKeyConfigFormFromPayload(payload));
    setAppKeyConfigErrors({});
  }

  async function loadModelDependencies(name: string) {
    const modelName = name.trim();
    if (!modelName) {
      setModelAncestry(null);
      setModelDependents(null);
      return { ancestry: null, dependents: null };
    }
    setDependencyError("");
    const encoded = adminModelPathName(modelName);
    const [ancestryPayload, dependentsPayload] = await Promise.all([
      requestJSON<ModelAncestryPayload>(`/admin/models/${encoded}/ancestry`, undefined, adminPassword),
      requestJSON<ModelDependentsPayload>(`/admin/models/${encoded}/dependents`, undefined, adminPassword)
    ]);
    setModelAncestry(ancestryPayload);
    setModelDependents(dependentsPayload);
    return { ancestry: ancestryPayload, dependents: dependentsPayload };
  }

  async function refreshRoutingState() {
    await loadRouting();
    await Promise.all([loadConfig().catch(() => undefined), loadOverview().catch(() => undefined)]);
  }

  function startModelCopy(name?: string) {
    const source = (name || "").trim();
    if (!source) return;
    setError("");
    setCopyModelForm({ source, target: nextCopyModelName(source, legacyModelNames), error: "" });
  }

  function cancelModelCopy() {
    setCopyModelForm(emptyCopyModelForm);
  }

  async function copyLegacyModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const source = copyModelForm.source.trim();
    const target = copyModelForm.target.trim();
    const formWithNames = { ...copyModelForm, source, target };
    if (!source || !target) {
      setCopyModelForm({ ...formWithNames, error: "Source and target are required" });
      return;
    }
    if (source === target) {
      setCopyModelForm({ ...formWithNames, error: "Target must differ from source" });
      return;
    }
    if (legacyModelNames.includes(target)) {
      setCopyModelForm({ ...formWithNames, error: `Model "${target}" already exists` });
      return;
    }
    if (modelTree.nodes?.[source]?.protected && !adminPassword.trim()) {
      const message = `Admin password required for protected model "${source}"`;
      setCopyModelForm({ ...formWithNames, error: message });
      setError(message);
      return;
    }
    setLoading(true);
    setError("");
    try {
      await requestJSON<JsonRecord>("/admin/models/copy", { method: "POST", body: JSON.stringify({ source, target }) }, adminPassword);
      setNotice(`Model "${target}" copied`);
      setCopyModelForm(emptyCopyModelForm);
      await refreshRoutingState();
      setDependencyModel(target);
      await loadModelDependencies(target).catch((err) => setDependencyError(errorMessage(err)));
    } catch (err) {
      const message = errorMessage(err);
      setCopyModelForm({ ...formWithNames, error: message });
      setError(message);
    } finally {
      setLoading(false);
    }
  }

  async function deleteLegacyModel(name?: string) {
    const modelName = (name || "").trim();
    if (!modelName) return;
    setError("");
    setDependencyError("");
    setDependencyModel(modelName);
    let dependentsPayload: ModelDependentsPayload | null = null;
    try {
      const loaded = await loadModelDependencies(modelName);
      dependentsPayload = loaded.dependents;
    } catch (err) {
      const message = errorMessage(err);
      setDependencyError(message);
      setError(message);
      return;
    }
    const directChildren = modelDependentsList(dependentsPayload, "direct_children");
    const descendants = modelDependentsList(dependentsPayload, "all_descendants");
    const protectedNames = protectedModelNames(modelTree, [modelName, ...descendants]);
    if (protectedNames.length > 0 && !adminPassword.trim()) {
      setError(protectedModelPasswordMessage(protectedNames));
      return;
    }
    const cascade = descendants.length > 0;
    if (!window.confirm(legacyModelDeleteConfirmMessage(modelName, directChildren, descendants, protectedNames, cascade))) {
      return;
    }
    await submitLegacyModelDelete(modelName, cascade, descendants);
  }

  async function submitLegacyModelDelete(modelName: string, cascade: boolean, descendants: string[]) {
    setLoading(true);
    setError("");
    try {
      await requestJSON<JsonRecord>(`/admin/models/${adminModelPathName(modelName)}${cascade ? "?cascade=true" : ""}`, { method: "DELETE" }, adminPassword);
      afterLegacyModelDelete(modelName, cascade, descendants);
      await refreshRoutingState();
    } catch (err) {
      const conflict = legacyModelDependentsConflict(err);
      if (!cascade && conflict) {
        setLoading(false);
        setModelDependents({
          model: modelName,
          direct_children: conflict.directChildren,
          all_descendants: conflict.descendants,
          descendant_count: conflict.descendants.length
        });
        const protectedNames = protectedModelNames(modelTree, [modelName, ...conflict.descendants]);
        if (protectedNames.length > 0 && !adminPassword.trim()) {
          setError(protectedModelPasswordMessage(protectedNames));
          return;
        }
        if (!window.confirm(legacyModelDeleteConfirmMessage(modelName, conflict.directChildren, conflict.descendants, protectedNames, true))) {
          setError(`Model "${modelName}" has dependents`);
          return;
        }
        setLoading(true);
        try {
          await requestJSON<JsonRecord>(`/admin/models/${adminModelPathName(modelName)}?cascade=true`, { method: "DELETE" }, adminPassword);
          afterLegacyModelDelete(modelName, true, conflict.descendants);
          await refreshRoutingState();
        } catch (cascadeErr) {
          setError(errorMessage(cascadeErr));
        }
        return;
      }
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  function afterLegacyModelDelete(modelName: string, cascade: boolean, descendants: string[]) {
    const deletedNames = [modelName, ...descendants];
    setNotice(cascade ? `Deleted "${modelName}" and ${descendantCountLabel(descendants.length)}` : `Deleted "${modelName}"`);
    setCopyModelForm((current) => (deletedNames.includes(current.source) || deletedNames.includes(current.target) ? emptyCopyModelForm : current));
    setDependencyModel((current) => (deletedNames.includes(current) ? "" : current));
  }

  async function validateConfigEditor() {
    setError("");
    try {
      const parsed = JSON.parse(configText) as JsonRecord;
      const payload = await requestJSON<ValidationPayload>("/admin/config/validate", { method: "POST", body: JSON.stringify(parsed) }, adminPassword);
      setConfigIssues(payload.issues || []);
      setValidationSummary({ errors: Number(payload.error_count || 0), warnings: Number(payload.warning_count || 0) });
      setNotice(payload.ok ? "Config valid" : "Config has issues");
      return payload;
    } catch (err) {
      const issue: ValidationIssue = { path: "$", severity: "error", code: "invalid_json", message: errorMessage(err) };
      setConfigIssues([issue]);
      setValidationSummary({ errors: 1, warnings: 0 });
      throw err;
    }
  }

  async function saveConfig() {
    setLoading(true);
    setError("");
    try {
      const validation = await validateConfigEditor();
      if (!validation.ok) {
        setError("Fix config validation errors");
        return;
      }
      const parsed = JSON.parse(configText) as JsonRecord;
      await requestJSON<JsonRecord>("/admin/config", { method: "PUT", body: JSON.stringify(parsed) }, adminPassword);
      setNotice("Saved");
      setConfigIssues([]);
      setValidationSummary({ errors: 0, warnings: 0 });
      await loadOverview();
    } catch (err) {
      const payload = apiErrorPayload(err) as ValidationPayload | undefined;
      if (payload?.issues) {
        setConfigIssues(payload.issues);
        setValidationSummary({ errors: Number(payload.error_count || 0), warnings: Number(payload.warning_count || 0) });
      }
      setError(payload?.issues ? "Fix config validation errors" : errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function reloadConfig() {
    setLoading(true);
    setError("");
    try {
      await requestJSON<JsonRecord>("/admin/config/reload", { method: "POST" }, adminPassword);
      await loadConfig();
      await loadOverview();
      setNotice("Reloaded");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function selectLog(id?: string) {
    if (!id) return;
    setError("");
    try {
      const payload = await requestJSON<JsonRecord>(`/admin/logs/${id}`, undefined, adminPassword);
      setSelectedLog(payload);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function registerModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextErrors = validateModelForm(modelForm);
    setModelErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix model form errors");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const payload: JsonRecord = {
        model_name: modelForm.model_name,
        api_base: modelForm.api_base,
        model: modelForm.model || modelForm.model_name,
        api_key: modelForm.api_key,
        api_type: modelForm.api_type || "openai",
        fallbacks: modelForm.fallbacks
          .split(",")
          .map((item) => item.trim())
          .filter(Boolean)
      };
      if (modelForm.api_type === "gigachat") {
        payload.mode = modelForm.mode || undefined;
        payload.scope = modelForm.scope || undefined;
        payload.auth_url = modelForm.auth_url || undefined;
        payload.client_id = modelForm.client_id || undefined;
        payload.client_secret = modelForm.client_secret || undefined;
        payload.client_cert = modelForm.client_cert || undefined;
        payload.client_key = modelForm.client_key || undefined;
        payload.verify_ssl = modelForm.verify_ssl;
        payload.emulate_tool_calls = modelForm.emulate_tool_calls;
      }
      await requestJSON<JsonRecord>("/admin/models", { method: "POST", body: JSON.stringify(payload) }, adminPassword);
      setNotice("Model saved");
      setModelForm({
        model_name: "",
        api_base: "",
        model: "",
        api_key: "",
        api_type: "openai",
        fallbacks: "",
        mode: "",
        scope: "",
        auth_url: "",
        client_id: "",
        client_secret: "",
        client_cert: "",
        client_key: "",
        verify_ssl: true,
        emulate_tool_calls: false
      });
      await loadConfig();
      await loadOverview();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function createProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextErrors = validateProviderForm(providerForm);
    setProviderErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix provider form errors");
      return;
    }
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      const endpoints = parseStringList(providerForm.endpoints_text);
      const payload: JsonRecord = {
        name: providerForm.name.trim(),
        api_base: providerForm.api_base.trim(),
        api_type: providerForm.api_type.trim()
      };
      if (endpoints.length > 0) {
        payload.endpoints = endpoints;
      }
      if (providerForm.api_key.trim()) {
        payload.api_key = providerForm.api_key;
      }
      await requestJSON<JsonRecord>("/admin/providers", { method: "POST", body: JSON.stringify(payload) }, adminPassword);
      setNotice("Provider created");
      setProviderForm({ name: "", api_base: "", api_type: "openai", api_key: "", endpoints_text: "", limits_text: "{}" });
      await refreshRoutingState();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function createNode(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextErrors = validateNodeForm(nodeForm);
    setNodeErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix node form errors");
      return;
    }
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      const endpoints = parseStringList(nodeForm.endpoints_text);
      const payload: JsonRecord = {
        name: nodeForm.name.trim(),
        provider: nodeForm.provider.trim(),
        model: nodeForm.model.trim()
      };
      if (nodeForm.api_base.trim()) {
        payload.api_base = nodeForm.api_base.trim();
      }
      if (endpoints.length > 0) {
        payload.endpoints = endpoints;
      }
      await requestJSON<JsonRecord>("/admin/nodes", { method: "POST", body: JSON.stringify(payload) }, adminPassword);
      setNotice("Node created");
      setNodeForm({ name: "", provider: "", model: "", api_base: "", endpoints_text: "", limits_text: "{}", modules_text: "{}", allowed_keys_text: "[]" });
      await refreshRoutingState();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function createClientModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextErrors = validateClientModelForm(clientModelForm);
    setClientModelErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix client model form errors");
      return;
    }
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      const payload: JsonRecord = {
        name: clientModelForm.name.trim(),
        nodes: clientModelForm.nodes
      };
      await requestJSON<JsonRecord>("/admin/client-models", { method: "POST", body: JSON.stringify(payload) }, adminPassword);
      setNotice("Client model created");
      setClientModelForm({ name: "", nodes: [] });
      await refreshRoutingState();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  function updateFallbackSource(rowID: string, source: string) {
    setFallbackError("");
    setRouterFallbacks((rows) =>
      rows.map((row) => row.id === rowID ? { ...row, source, targets: row.targets.filter((target) => target !== source) } : row)
    );
  }

  function toggleFallbackTarget(rowID: string, target: string, checked: boolean) {
    setFallbackError("");
    setRouterFallbacks((rows) =>
      rows.map((row) => row.id === rowID ? { ...row, targets: toggleStringSelection(row.targets, target, checked) } : row)
    );
  }

  function removeFallbackRow(rowID: string) {
    setFallbackError("");
    setRouterFallbacks((rows) => rows.filter((row) => row.id !== rowID));
  }

  function addFallbackRow(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const source = newFallback.source.trim();
    const targets = uniqueStrings(newFallback.targets.filter((target) => target !== source));
    if (!source) {
      setFallbackError("Choose a source model");
      return;
    }
    if (targets.length === 0) {
      setFallbackError("Choose at least one fallback target");
      return;
    }
    if (routerFallbacks.some((row) => row.source === source)) {
      setFallbackError(`Fallback source "${source}" already exists`);
      return;
    }
    setRouterFallbacks((rows) => [...rows, { id: nextFallbackRowID(), source, targets }]);
    setNewFallback({ source: "", targets: [] });
    setFallbackError("");
  }

  async function saveRouterFallbacks(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validationError = validateFallbackRows(routerFallbacks);
    if (validationError) {
      setFallbackError(validationError);
      setError("Fix router fallback errors");
      return;
    }
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      await requestJSON<JsonRecord>("/admin/router-settings", { method: "PUT", body: JSON.stringify({ fallbacks: fallbackPayloadFromRows(routerFallbacks) }) }, adminPassword);
      setNotice("Router fallbacks updated");
      await refreshRoutingState();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function deleteProvider(name?: string) {
    await deleteRoutingResource("provider", "/admin/providers", name);
  }

  async function deleteNode(name?: string) {
    await deleteRoutingResource("node", "/admin/nodes", name);
  }

  async function deleteClientModel(name?: string) {
    await deleteRoutingResource("client model", "/admin/client-models", name);
  }

  function startProviderEdit(provider: ProviderRecord) {
    setError("");
    setRoutingConflict("");
    setRoutingEdit({
      kind: "provider",
      original: provider,
      form: {
        name: provider.name || "",
        api_base: provider.api_base || "",
        api_type: provider.api_type || "openai",
        api_key: "",
        endpoints_text: formatStringList(asArray<string>(provider.endpoints)),
        limits_text: formatJSONText(isJsonRecord(provider.limits) ? provider.limits : {})
      },
      errors: {}
    });
  }

  function startNodeEdit(node: NodeRecord) {
    setError("");
    setRoutingConflict("");
    setRoutingEdit({
      kind: "node",
      original: node,
      form: {
        name: node.name || "",
        provider: node.provider || "",
        model: node.model || "",
        api_base: node.api_base || "",
        endpoints_text: formatStringList(asArray<string>(node.endpoints)),
        limits_text: formatJSONText(isJsonRecord(node.limits) ? node.limits : {}),
        modules_text: formatJSONText(isJsonRecord(node.modules) ? node.modules : {}),
        allowed_keys_text: formatJSONText(node.allowed_keys ?? [])
      },
      errors: {}
    });
  }

  function startClientModelEdit(clientModel: ClientModelRecord) {
    setError("");
    setRoutingConflict("");
    setRoutingEdit({
      kind: "client-model",
      original: clientModel,
      form: {
        name: resourceName(clientModel),
        nodes_text: formatStringList(asArray<string>(clientModel.nodes)),
        node_weights_text: formatNodeWeights(clientModel.node_weights),
        balancing: typeof clientModel.balancing === "string" ? clientModel.balancing : "",
        limits_text: formatJSONText(isJsonRecord(clientModel.limits) ? clientModel.limits : {}),
        allowed_keys_text: formatJSONText(clientModel.allowed_keys ?? [])
      },
      errors: {}
    });
  }

  function cancelRoutingEdit() {
    setRoutingEdit(null);
  }

  async function updateProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!routingEdit || routingEdit.kind !== "provider") return;
    const limits = parseJSONField<LimitsRecord>(routingEdit.form.limits_text, "Limits JSON", "object");
    const nextErrors = validateProviderForm(routingEdit.form);
    if (limits.error) {
      nextErrors.limits_text = limits.error;
    }
    setRoutingEdit({ ...routingEdit, errors: nextErrors });
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix provider form errors");
      return;
    }
    const payload: JsonRecord = changedStringFields(routingEdit.original, routingEdit.form, ["api_base", "api_type"]);
    const endpoints = parseStringList(routingEdit.form.endpoints_text);
    if (!arraysEqual(asArray<string>(routingEdit.original.endpoints), endpoints)) {
      payload.endpoints = endpoints;
    }
    if (routingEdit.form.api_key.trim()) {
      payload.api_key = routingEdit.form.api_key;
    }
    if (!jsonEqual(isJsonRecord(routingEdit.original.limits) ? routingEdit.original.limits : {}, limits.value)) {
      payload.limits = limits.value;
    }
    await updateRoutingResource("provider", "/admin/providers", routingEdit.form.name, payload);
  }

  async function updateNode(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!routingEdit || routingEdit.kind !== "node") return;
    const limits = parseJSONField<LimitsRecord>(routingEdit.form.limits_text, "Limits JSON", "object");
    const modules = parseJSONField<JsonRecord>(routingEdit.form.modules_text, "Modules JSON", "object");
    const allowedKeys = parseAllowedKeysField(routingEdit.form.allowed_keys_text, "Allowed keys JSON");
    const nextErrors = validateNodeForm(routingEdit.form);
    if (limits.error) {
      nextErrors.limits_text = limits.error;
    }
    if (modules.error) {
      nextErrors.modules_text = modules.error;
    }
    if (allowedKeys.error) {
      nextErrors.allowed_keys_text = allowedKeys.error;
    }
    setRoutingEdit({ ...routingEdit, errors: nextErrors });
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix node form errors");
      return;
    }
    const payload: JsonRecord = changedStringFields(routingEdit.original, routingEdit.form, ["provider", "model", "api_base"]);
    const endpoints = parseStringList(routingEdit.form.endpoints_text);
    if (!arraysEqual(asArray<string>(routingEdit.original.endpoints), endpoints)) {
      payload.endpoints = endpoints;
    }
    if (!jsonEqual(isJsonRecord(routingEdit.original.limits) ? routingEdit.original.limits : {}, limits.value)) {
      payload.limits = limits.value;
    }
    if (!jsonEqual(isJsonRecord(routingEdit.original.modules) ? routingEdit.original.modules : {}, modules.value)) {
      payload.modules = modules.value;
    }
    if (!jsonEqual(routingEdit.original.allowed_keys ?? [], allowedKeys.value)) {
      payload.allowed_keys = allowedKeys.value;
    }
    await updateRoutingResource("node", "/admin/nodes", routingEdit.form.name, payload);
  }

  async function updateClientModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!routingEdit || routingEdit.kind !== "client-model") return;
    const nodes = parseNodeList(routingEdit.form.nodes_text);
    const weightsResult = parseNodeWeights(routingEdit.form.node_weights_text);
    const limits = parseJSONField<LimitsRecord>(routingEdit.form.limits_text, "Limits JSON", "object");
    const allowedKeys = parseAllowedKeysField(routingEdit.form.allowed_keys_text, "Allowed keys JSON");
    const nextErrors = validateClientModelEditForm(routingEdit.form, nodes, weightsResult);
    if (limits.error) {
      nextErrors.limits_text = limits.error;
    }
    if (allowedKeys.error) {
      nextErrors.allowed_keys_text = allowedKeys.error;
    }
    setRoutingEdit({ ...routingEdit, errors: nextErrors });
    if (Object.keys(nextErrors).length > 0) {
      setError("Fix client model form errors");
      return;
    }
    const originalNodes = asArray<string>(routingEdit.original.nodes);
    const payload: JsonRecord = arraysEqual(originalNodes, nodes) ? {} : { nodes };
    const originalWeights = asIntegerRecord(routingEdit.original.node_weights);
    const nextWeights = weightsResult.weights;
    if (!integerRecordsEqual(originalWeights, nextWeights)) {
      payload.node_weights = nextWeights;
    }
    const nextBalancing = routingEdit.form.balancing.trim();
    const originalBalancing = typeof routingEdit.original.balancing === "string" ? routingEdit.original.balancing : "";
    if (nextBalancing !== originalBalancing) {
      payload.balancing = nextBalancing;
    }
    if (!jsonEqual(isJsonRecord(routingEdit.original.limits) ? routingEdit.original.limits : {}, limits.value)) {
      payload.limits = limits.value;
    }
    if (!jsonEqual(routingEdit.original.allowed_keys ?? [], allowedKeys.value)) {
      payload.allowed_keys = allowedKeys.value;
    }
    await updateRoutingResource("client model", "/admin/client-models", routingEdit.form.name, payload);
  }

  async function updateRoutingResource(label: string, endpoint: string, name: string, payload: JsonRecord) {
    if (!name) return;
    if (Object.keys(payload).length === 0) {
      setNotice("No changes");
      setError("");
      return;
    }
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      await requestJSON<JsonRecord>(`${endpoint}/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify(payload) }, adminPassword);
      setNotice(`${titleCase(label)} updated`);
      setRoutingEdit(null);
      await refreshRoutingState();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function deleteRoutingResource(label: string, endpoint: string, name?: string) {
    if (!name) return;
    if (!window.confirm(`Delete ${label} "${name}"?`)) return;
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      await requestJSON<JsonRecord>(`${endpoint}/${encodeURIComponent(name)}`, { method: "DELETE" }, adminPassword);
      setNotice(`${titleCase(label)} deleted`);
      await refreshRoutingState();
    } catch (err) {
      const message = deleteConflictMessage(err, label, name);
      setRoutingConflict(message);
      setError(message);
    } finally {
      setLoading(false);
    }
  }

  async function createAppKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const result = appKeyPayloadFromForm(appKeyForm, null, false);
    setAppKeyErrors(result.errors);
    if (Object.keys(result.errors).length > 0) {
      setError("Fix app key form errors");
      return;
    }
    setLoading(true);
    setError("");
    setSecretReveal(null);
    try {
      const response = await requestJSON<AppKeyMutationPayload>("/admin/keys", { method: "POST", body: JSON.stringify(result.payload) }, adminPassword);
      setNotice("App key created");
      setAppKeyForm(emptyAppKeyForm);
      setSecretReveal(secretRevealFromResponse(response));
      await loadAppKeys();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  function startAppKeyEdit(key: AppKeyRecord) {
    setError("");
    setSecretReveal(null);
    setAppKeyEdit({ original: key, form: appKeyFormFromRecord(key), errors: {} });
  }

  function cancelAppKeyEdit() {
    setAppKeyEdit(null);
  }

  async function updateAppKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!appKeyEdit) return;
    const keyID = keyIDFromRecord(appKeyEdit.original);
    const result = appKeyPayloadFromForm(appKeyEdit.form, appKeyEdit.original, true);
    setAppKeyEdit({ ...appKeyEdit, errors: result.errors });
    if (!keyID || Object.keys(result.errors).length > 0) {
      setError(keyID ? "Fix app key form errors" : "App key ID is missing");
      return;
    }
    if (Object.keys(result.payload).length === 0) {
      setNotice("No changes");
      setError("");
      return;
    }
    setLoading(true);
    setError("");
    setSecretReveal(null);
    try {
      await requestJSON<AppKeyMutationPayload>(`/admin/keys/${encodeURIComponent(keyID)}`, { method: "PUT", body: JSON.stringify(result.payload) }, adminPassword);
      setNotice("App key updated");
      setAppKeyEdit(null);
      await loadAppKeys();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function regenerateAppKey(keyID?: string) {
    if (!keyID) return;
    if (!window.confirm(`Regenerate secret for "${keyID}"? The previous secret will stop working immediately.`)) return;
    setLoading(true);
    setError("");
    setSecretReveal(null);
    try {
      const response = await requestJSON<AppKeyMutationPayload>(`/admin/keys/${encodeURIComponent(keyID)}/regenerate`, { method: "POST", body: "{}" }, adminPassword);
      setNotice("App key secret regenerated");
      setSecretReveal(secretRevealFromResponse(response));
      await loadAppKeys();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function deleteAppKey(keyID?: string) {
    if (!keyID) return;
    if (!window.confirm(`Delete app key "${keyID}"?`)) return;
    setLoading(true);
    setError("");
    setSecretReveal(null);
    try {
      await requestJSON<JsonRecord>(`/admin/keys/${encodeURIComponent(keyID)}`, { method: "DELETE" }, adminPassword);
      setNotice("App key deleted");
      if (keyIDFromRecord(appKeyEdit?.original || {}) === keyID) {
        setAppKeyEdit(null);
      }
      await loadAppKeys();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  async function saveAppKeysConfig(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const result = appKeysConfigPayloadFromForm(appKeyConfigForm);
    setAppKeyConfigErrors(result.errors);
    if (Object.keys(result.errors).length > 0) {
      setError("Fix app key config errors");
      return;
    }
    setLoading(true);
    setError("");
    setSecretReveal(null);
    try {
      const payload = await requestJSON<AppKeysPayload>("/admin/keys/config", { method: "POST", body: JSON.stringify(result.payload) }, adminPassword);
      setNotice("App key config saved");
      setAppKeys(asArray<AppKeyRecord>(payload.keys));
      setAppKeysConfig(payload);
      setAppKeyConfigForm(appKeyConfigFormFromPayload(payload));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <main className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">
            <Shield size={21} aria-hidden="true" />
          </div>
          <div>
            <h1>yaLLMproxy</h1>
            <p>Admin</p>
          </div>
        </div>
        <nav className="nav">
          {tabs.map((tab) => {
            const Icon = tab.icon;
            return (
              <button key={tab.id} className={activeTab === tab.id ? "nav-item active" : "nav-item"} onClick={() => switchTab(tab.id)}>
                <Icon size={18} aria-hidden="true" />
                <span>{tab.label}</span>
              </button>
            );
          })}
        </nav>
        <label className="field compact-field">
          <span>Admin password</span>
          <input type="password" value={adminPassword} onChange={(event) => setAdminPassword(event.target.value)} autoComplete="current-password" />
        </label>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <div>
            <h2>{tabs.find((tab) => tab.id === activeTab)?.label}</h2>
            <p>{statusLine(health.length, healthyCount, logMeta.total)}</p>
          </div>
          <div className="toolbar">
            {notice && <Badge tone="good">{notice}</Badge>}
            {error && <Badge tone="bad">{error}</Badge>}
            <Button variant="secondary" size="icon" onClick={() => void refreshAll()} disabled={loading} title="Refresh">
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </header>

        {activeTab === "overview" && (
          <div className="view-grid">
            <section className="metric-grid">
              <Metric icon={HeartPulse} label="Healthy nodes" value={`${healthyCount}/${health.length}`} />
              <Metric icon={Gauge} label="Active requests" value={formatValue(usage.active_requests ?? usage.active ?? 0)} />
              <Metric icon={Database} label="Quota scopes" value={quotaRows.length.toString()} />
              <Metric icon={ListFilter} label="Tool call rate" value={percentValue(toolRate.tool_call_rate)} />
            </section>

            <section className="panel wide">
              <PanelTitle icon={HeartPulse} title="Backend Health" />
              <DataTable
                columns={["name", "healthy", "score", "successes", "failures", "last_error"]}
                rows={health}
                empty="No backend health data"
                renderCell={(row, column) => renderHealthCell(row, column)}
              />
            </section>

            <section className="panel">
              <PanelTitle icon={Gauge} title="Quota" />
              <DataTable columns={["scope", "used"]} rows={quotaRows} empty="No quota usage" renderCell={(row, column) => formatValue(row[column])} />
            </section>

            <section className="panel">
              <PanelTitle icon={Activity} title="Stop Reasons" />
              <DataTable columns={["reason", "count", "percentage"]} rows={stopReasons} empty="No stop reason data" renderCell={(row, column) => column === "percentage" ? percentValue(row[column]) : formatValue(row[column])} />
            </section>

            <section className="panel wide">
              <PanelTitle icon={Server} title="Model Traffic" />
              <DataTable
                columns={["model_name", "total_requests", "stop_reasons"]}
                rows={modelStats}
                empty="No model traffic data"
                renderCell={(row, column) => column === "stop_reasons" ? compactJSON(row[column]) : formatValue(row[column])}
              />
            </section>
          </div>
        )}

        {activeTab === "observability" && (
          <div className="view-grid observability-view">
            <section className="metric-grid">
              <Metric icon={Gauge} label="Cache hit rate" value={percentValue(cacheHitRate(cacheStats))} />
              <Metric icon={HardDrive} label="Cache entries" value={cacheEntriesValue(cacheStats)} />
              <Metric icon={RefreshCw} label="Cache evictions" value={formatValue(cacheStats.evictions ?? 0)} />
              <Metric icon={Activity} label="Active requests" value={formatValue(usage.active_requests ?? usage.active ?? 0)} />
            </section>

            <section className="panel wide">
              <div className="panel-header">
                <PanelTitle icon={HardDrive} title="Inference Cache" />
                <div className="toolbar">
                  {observabilityLoading && <Badge tone="warn">loading</Badge>}
                  <Badge tone={cacheEnabledTone(cacheStats)}>{cacheEnabledLabel(cacheStats)}</Badge>
                  <Badge tone={cacheModeTone(cacheStats.mode)}>{cacheModeLabel(cacheStats.mode)}</Badge>
                </div>
              </div>
              <CacheStatus error={cacheError} status={cacheStatus} stats={cacheStats} loading={observabilityLoading} />
              <DataTable
                columns={["field", "value"]}
                rows={cacheRows}
                empty={cacheTableEmpty(cacheError, cacheStatus, observabilityLoading)}
                renderCell={(row, column) => column === "value" ? renderCacheValue(row) : formatValue(row[column])}
              />
            </section>

            <section className="panel">
              <div className="panel-header">
                <PanelTitle icon={HeartPulse} title="Backend Health" />
                <Badge tone={healthyCount === health.length ? "good" : "warn"}>{healthyCount}/{health.length} up</Badge>
              </div>
              <DataTable
                columns={["name", "healthy", "score", "successes", "failures", "last_error"]}
                rows={health}
                empty="No backend health data"
                renderCell={(row, column) => renderHealthCell(row, column)}
              />
            </section>

            <section className="panel">
              <div className="panel-header">
                <PanelTitle icon={Gauge} title="Usage And Quota" />
                <Badge>{quotaRows.length} scopes</Badge>
              </div>
              <DataTable columns={["scope", "used"]} rows={quotaRows} empty="No quota usage" renderCell={(row, column) => formatValue(row[column])} />
            </section>

            {observabilityTraceID && (
              <section className="panel wide">
                <div className="panel-header">
                  <PanelTitle icon={BarChart3} title="Trace" />
                  {renderTraceID(observabilityTraceID)}
                </div>
              </section>
            )}
          </div>
        )}

        {activeTab === "catalog" && (
          <div className="catalog-view">
            <section className="panel wide">
              <div className="panel-header">
                <PanelTitle icon={ListFilter} title="Model Catalog" />
                <div className="toolbar">
                  {catalogLoading && <Badge tone="warn">loading</Badge>}
                  <Badge tone={catalog?.enabled ? "good" : "neutral"}>{catalog?.enabled ? "enabled" : "disabled"}</Badge>
                  <Badge tone={catalog?.probe_enabled ? "good" : "neutral"}>{catalog?.probe_enabled ? "probes on" : "probes off"}</Badge>
                  <Badge>{catalogRows.length} nodes</Badge>
                  <Button variant="secondary" size="icon" onClick={() => void loadCatalog()} title="Reload catalog">
                    <RefreshCw size={17} aria-hidden="true" />
                  </Button>
                </div>
              </div>
              <CatalogStatus error={catalogError} status={catalogStatus} snapshot={catalog} loading={catalogLoading} />
              <div className="catalog-meta">
                <span>Generated</span>
                <strong>{formatTime(catalog?.generated_at)}</strong>
              </div>
              <CatalogWarnings warnings={catalogWarnings} />
              <DataTable
                columns={["model", "provider", "node", "status", "source", "error_class", "configured_model", "target_model", "last_sync", "next_sync"]}
                rows={catalogRows}
                empty={catalogTableEmpty(catalog, catalogError, catalogStatus, catalogLoading)}
                renderCell={(row, column) => renderCatalogCell(row as CatalogRow, column)}
              />
            </section>
          </div>
        )}

        {activeTab === "traces" && (
          <div className="traces-view">
            <section className="panel wide">
              <div className="panel-header">
                <PanelTitle icon={GitBranch} title="Trace Lookup" />
                <div className="toolbar">
                  {traceLoading && <Badge tone="warn">loading</Badge>}
                  {traceDetail?.trace_id && renderTraceID(traceDetail.trace_id)}
                  <Button variant="secondary" size="icon" onClick={() => void loadTraceDashboard()} disabled={!traceInput.trim() || traceLoading} title="Reload trace">
                    <RefreshCw size={17} aria-hidden="true" />
                  </Button>
                </div>
              </div>
              <form className="trace-search-form" onSubmit={submitTraceSearch}>
                <label className="field trace-search-field">
                  <span>Trace ID</span>
                  <input value={traceInput} onChange={(event) => setTraceInput(event.target.value)} autoComplete="off" />
                </label>
                <Button size="icon" disabled={traceLoading || !traceInput.trim()} title="Load trace">
                  <Search size={17} aria-hidden="true" />
                </Button>
              </form>
              <TraceStatus error={traceError} status={traceStatus} detail={traceDetail} loading={traceLoading} />
            </section>

            {traceDetail && (
              <>
                <section className="metric-grid trace-metrics">
                  <Metric icon={GitBranch} label="Requests" value={formatValue(traceDetail.request_count ?? traceDetail.total ?? traceRequests.length)} />
                  <Metric icon={Network} label="Attempts" value={formatValue(traceAttempts.length)} />
                  <Metric icon={Activity} label="Errors" value={formatValue(traceErrors.length)} />
                  <Metric icon={Clock3} label="Total duration" value={formatDuration(traceDetail.duration_ms)} />
                </section>

                <section className="panel wide">
                  <div className="panel-header">
                    <PanelTitle icon={Gauge} title="Trace Summary" />
                    <div className="toolbar">
                      <Badge tone={traceDetail.has_more ? "warn" : "neutral"}>{traceDetail.has_more ? "partial" : "complete"}</Badge>
                      <Badge>{traceDetail.returned ?? traceRequests.length} returned</Badge>
                    </div>
                  </div>
                  <div className="trace-summary-grid">
                    <TraceSummaryValue label="Started" value={formatTime(traceDetail.started_at)} />
                    <TraceSummaryValue label="Ended" value={formatTime(traceDetail.ended_at)} />
                    <TraceSummaryValue label="Models" value={renderStringBadges(asArray<string>(traceDetail.models))} />
                    <TraceSummaryValue label="Backends" value={renderStringBadges(asArray<string>(traceDetail.backends))} />
                    <TraceSummaryValue label="Outcomes" value={renderTraceOutcomeBadges(asArray<string>(traceDetail.outcomes))} />
                  </div>
                </section>

                <section className="panel wide">
                  <div className="panel-header">
                    <PanelTitle icon={Clock3} title="Timeline" />
                    <Badge>{traceTimeline.length} events</Badge>
                  </div>
                  <TraceTimeline items={traceTimeline} />
                </section>

                <section className="trace-grid">
                  <div className="panel">
                    <div className="panel-header">
                      <PanelTitle icon={Network} title="Attempts" />
                      <Badge>{traceAttempts.length} rows</Badge>
                    </div>
                    <DataTable
                      columns={["request_id", "attempt_index", "retry_index", "backend_name", "status", "outcome", "retryable", "duration_ms", "error_type"]}
                      rows={traceAttempts as JsonRecord[]}
                      empty="No attempts returned"
                      renderCell={(row, column) => renderTraceAttemptCell(row as TraceAttemptRecord, column)}
                    />
                  </div>

                  <div className="panel">
                    <div className="panel-header">
                      <PanelTitle icon={Activity} title="Linked Errors" />
                      <Badge>{traceErrors.length} rows</Badge>
                    </div>
                    <DataTable
                      columns={["timestamp", "request_log_id", "error_type", "model_name", "backend_name", "http_status"]}
                      rows={traceErrors as JsonRecord[]}
                      empty="No linked errors"
                      renderCell={(row, column) => renderTraceErrorCell(row as TraceErrorRecord, column)}
                    />
                  </div>
                </section>
              </>
            )}
          </div>
        )}

        {activeTab === "logs" && (
          <div className="split-view">
            <section className="panel">
              <div className="panel-header">
                <PanelTitle icon={Database} title="Request Logs" />
                <Badge>{logMeta.hasMore ? `${logMeta.total}+` : logMeta.total} rows</Badge>
              </div>
              <div className="filter-row">
                <input placeholder="model" value={filters.model} onChange={(event) => setFilters({ ...filters, model: event.target.value })} />
                <select value={filters.outcome} onChange={(event) => setFilters({ ...filters, outcome: event.target.value })}>
                  <option value="">outcome</option>
                  <option value="success">success</option>
                  <option value="error">error</option>
                </select>
                <input placeholder="search" value={filters.search} onChange={(event) => setFilters({ ...filters, search: event.target.value })} />
                <Button variant="secondary" size="icon" onClick={() => void loadLogs()} title="Apply filters">
                  <ListFilter size={17} aria-hidden="true" />
                </Button>
              </div>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>time</th>
                      <th>model</th>
                      <th>status</th>
                      <th>tokens</th>
                      <th>duration</th>
                      {logsHaveTraceIDs && <th>trace</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {logs.map((row, index) => (
                      <tr key={row.id || index} onClick={() => void selectLog(row.id)} className="clickable-row">
                        <td>{formatTime(row.request_time || row.created_at)}</td>
                        <td>{row.model_name || "-"}</td>
                        <td>
                          <Badge tone={outcomeTone(row)}>{row.outcome || row.backend_status || "-"}</Badge>
                          {Number(row.warning_count || 0) > 0 && <Badge tone="warn">{row.warning_count} warn</Badge>}
                        </td>
                        <td>{formatValue(row.total_tokens)}</td>
                        <td>{formatDuration(row.duration_ms)}</td>
                        {logsHaveTraceIDs && <td>{renderTraceID(traceIDFromRecord(row), openTraceDashboard)}</td>}
                      </tr>
                    ))}
                    {logs.length === 0 && (
                      <tr>
                        <td colSpan={logsHaveTraceIDs ? 6 : 5} className="empty-cell">No logs returned</td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
            </section>

            <section className="panel detail-panel">
              <div className="panel-header">
                <PanelTitle icon={FileJson} title="Log Detail" />
                {selectedLog && traceIDFromRecord(selectedLog) && renderTraceID(traceIDFromRecord(selectedLog), openTraceDashboard)}
              </div>
              <pre>{selectedLog ? JSON.stringify(selectedLog, null, 2) : "No log selected"}</pre>
            </section>
          </div>
        )}

        {activeTab === "config" && (
          <section className="panel editor-panel">
            <div className="panel-header">
              <PanelTitle icon={FileJson} title="Raw Config" />
              <div className="toolbar">
                <Button variant="secondary" size="icon" onClick={() => void loadConfig()} title="Reload editor">
                  <RefreshCw size={17} aria-hidden="true" />
                </Button>
                <Button variant="secondary" size="icon" onClick={() => void validateConfigEditor().catch((err) => setError(errorMessage(err)))} title="Validate config">
                  <CheckCircle2 size={17} aria-hidden="true" />
                </Button>
                <Button variant="secondary" size="icon" onClick={() => void reloadConfig()} title="Reload from disk">
                  <UploadCloud size={17} aria-hidden="true" />
                </Button>
                <Button size="icon" onClick={() => void saveConfig()} disabled={loading} title="Save config">
                  <Save size={17} aria-hidden="true" />
                </Button>
              </div>
            </div>
            <textarea className="config-editor" value={configText} onChange={(event) => setConfigText(event.target.value)} spellCheck={false} />
            <ValidationIssues issues={configIssues} summary={validationSummary} />
          </section>
        )}

        {activeTab === "models" && (
          <div className="models-view">
            <section className="panel wide">
              <div className="panel-header">
                <PanelTitle icon={Server} title="Configured Models" />
                <Badge>{models.length} models</Badge>
              </div>
              <DataTable
                columns={["id", "supports_reasoning", "tags"]}
                rows={models as JsonRecord[]}
                empty="No configured models"
                renderCell={(row, column) => column === "supports_reasoning" ? renderBooleanBadge(row[column]) : formatValue(row[column])}
              />
            </section>

            <section className="panel model-panel">
              <PanelTitle icon={KeyRound} title="Register Model" />
              <form className="model-form" onSubmit={(event) => void registerModel(event)}>
                <label className="field">
                  <span>Model name</span>
                  <input required value={modelForm.model_name} onChange={(event) => setModelForm({ ...modelForm, model_name: event.target.value })} />
                  <FieldError message={modelErrors.model_name} />
                </label>
                <label className="field">
                  <span>API base</span>
                  <input required value={modelForm.api_base} onChange={(event) => setModelForm({ ...modelForm, api_base: event.target.value })} />
                  <FieldError message={modelErrors.api_base} />
                </label>
                <label className="field">
                  <span>Upstream model</span>
                  <input value={modelForm.model} onChange={(event) => setModelForm({ ...modelForm, model: event.target.value })} />
                </label>
                <label className="field">
                  <span>API key</span>
                  <input type="password" value={modelForm.api_key} onChange={(event) => setModelForm({ ...modelForm, api_key: event.target.value })} />
                </label>
                <label className="field">
                  <span>API type</span>
                  <select value={modelForm.api_type} onChange={(event) => setModelForm({ ...modelForm, api_type: event.target.value })}>
                    <option value="openai">openai</option>
                    <option value="anthropic">anthropic</option>
                    <option value="gigachat">gigachat</option>
                    <option value="passthrough">passthrough</option>
                  </select>
                  <FieldError message={modelErrors.api_type} />
                </label>
                {modelForm.api_type === "gigachat" && (
                  <>
                    <label className="field">
                      <span>Mode</span>
                      <select value={modelForm.mode} onChange={(event) => setModelForm({ ...modelForm, mode: event.target.value })}>
                        <option value="">auto</option>
                        <option value="cloud">cloud</option>
                        <option value="local">local</option>
                      </select>
                    </label>
                    <label className="field">
                      <span>Scope</span>
                      <input value={modelForm.scope} placeholder="GIGACHAT_API_CORP" onChange={(event) => setModelForm({ ...modelForm, scope: event.target.value })} />
                    </label>
                    <label className="field">
                      <span>Auth URL</span>
                      <input value={modelForm.auth_url} placeholder="https://ngw.devices.sberbank.ru:9443/api/v2/oauth" onChange={(event) => setModelForm({ ...modelForm, auth_url: event.target.value })} />
                      <FieldError message={modelErrors.auth_url} />
                    </label>
                    <label className="field">
                      <span>Client ID</span>
                      <input value={modelForm.client_id} onChange={(event) => setModelForm({ ...modelForm, client_id: event.target.value })} />
                    </label>
                    <label className="field">
                      <span>Client secret</span>
                      <input type="password" value={modelForm.client_secret} onChange={(event) => setModelForm({ ...modelForm, client_secret: event.target.value })} />
                      <FieldError message={modelErrors.client_secret} />
                    </label>
                    <label className="field">
                      <span>Client cert</span>
                      <input value={modelForm.client_cert} onChange={(event) => setModelForm({ ...modelForm, client_cert: event.target.value })} />
                      <FieldError message={modelErrors.client_cert} />
                    </label>
                    <label className="field">
                      <span>Client key</span>
                      <input value={modelForm.client_key} onChange={(event) => setModelForm({ ...modelForm, client_key: event.target.value })} />
                    </label>
                    <label className="field checkbox-field">
                      <input type="checkbox" checked={modelForm.verify_ssl} onChange={(event) => setModelForm({ ...modelForm, verify_ssl: event.target.checked })} />
                      <span>Verify SSL</span>
                    </label>
                    <label className="field checkbox-field">
                      <input type="checkbox" checked={modelForm.emulate_tool_calls} onChange={(event) => setModelForm({ ...modelForm, emulate_tool_calls: event.target.checked })} />
                      <span>Emulate tool calls</span>
                    </label>
                  </>
                )}
                <label className="field">
                  <span>Fallbacks</span>
                  <input value={modelForm.fallbacks} onChange={(event) => setModelForm({ ...modelForm, fallbacks: event.target.value })} />
                </label>
                <div className="form-actions">
                  <Button type="submit" disabled={loading}>
                    <Save size={16} aria-hidden="true" />
                    Save
                  </Button>
                </div>
              </form>
            </section>
          </div>
        )}

        {activeTab === "routing" && (
          <div className="routing-view">
            {routingConflict && (
              <section className="routing-alert">
                <Badge tone="bad">Delete blocked</Badge>
                <span>{routingConflict}</span>
              </section>
            )}

            <section className="routing-block">
              <section className="panel routing-list-panel">
                <div className="panel-header">
                  <PanelTitle icon={KeyRound} title="Providers" />
                  <Badge>{providers.length} providers</Badge>
                </div>
                <DataTable
                  columns={["name", "api_base", "api_type", "endpoints", "actions"]}
                  rows={providers as JsonRecord[]}
                  empty="No providers"
                  renderCell={(row, column) => renderProviderCell(row, column, loading, startProviderEdit, deleteProvider)}
                />
                {routingEdit?.kind === "provider" && (
                  <form className="routing-edit-form" onSubmit={(event) => void updateProvider(event)}>
                    <div className="routing-edit-header">
                      <span className="resource-name">{routingEdit.form.name}</span>
                      <Button type="button" variant="ghost" size="icon" onClick={cancelRoutingEdit} title="Cancel edit">
                        <X size={16} aria-hidden="true" />
                      </Button>
                    </div>
                    <label className="field">
                      <span>Name</span>
                      <input value={routingEdit.form.name} readOnly aria-readonly="true" />
                    </label>
                    <label className="field">
                      <span>API base</span>
                      <input
                        required
                        value={routingEdit.form.api_base}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, api_base: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.api_base} />
                    </label>
                    <label className="field">
                      <span>API type</span>
                      <input
                        required
                        list="routing-api-types"
                        value={routingEdit.form.api_type}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, api_type: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.api_type} />
                    </label>
                    <label className="field">
                      <span>Replace API key</span>
                      <input
                        type="password"
                        autoComplete="off"
                        value={routingEdit.form.api_key}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, api_key: event.target.value } })}
                      />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Endpoints</span>
                      <textarea
                        className="routing-list-textarea"
                        value={routingEdit.form.endpoints_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, endpoints_text: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.endpoints_text} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Limits</span>
                      <textarea
                        className="routing-list-textarea json-textarea"
                        value={routingEdit.form.limits_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, limits_text: event.target.value } })}
                        spellCheck={false}
                      />
                      <FieldError message={routingEdit.errors.limits_text} />
                    </label>
                    <div className="form-actions">
                      <Button type="submit" disabled={loading}>
                        <Save size={16} aria-hidden="true" />
                        Save
                      </Button>
                    </div>
                  </form>
                )}
              </section>

              <section className="panel">
                <PanelTitle icon={Plus} title="New Provider" />
                <form className="model-form routing-form" onSubmit={(event) => void createProvider(event)}>
                  <label className="field">
                    <span>Name</span>
                    <input required value={providerForm.name} onChange={(event) => setProviderForm({ ...providerForm, name: event.target.value })} />
                    <FieldError message={providerErrors.name} />
                  </label>
                  <label className="field">
                    <span>API base</span>
                    <input required value={providerForm.api_base} onChange={(event) => setProviderForm({ ...providerForm, api_base: event.target.value })} />
                    <FieldError message={providerErrors.api_base} />
                  </label>
                  <label className="field">
                    <span>API type</span>
                    <input required list="routing-api-types" value={providerForm.api_type} onChange={(event) => setProviderForm({ ...providerForm, api_type: event.target.value })} />
                    <datalist id="routing-api-types">
                      <option value="openai" />
                      <option value="anthropic" />
                      <option value="gigachat" />
                      <option value="passthrough" />
                    </datalist>
                    <FieldError message={providerErrors.api_type} />
                  </label>
                  <label className="field">
                    <span>API key</span>
                    <input type="password" autoComplete="off" value={providerForm.api_key} onChange={(event) => setProviderForm({ ...providerForm, api_key: event.target.value })} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Endpoints</span>
                    <textarea
                      className="routing-list-textarea"
                      value={providerForm.endpoints_text}
                      onChange={(event) => setProviderForm({ ...providerForm, endpoints_text: event.target.value })}
                    />
                    <FieldError message={providerErrors.endpoints_text} />
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading}>
                      <Plus size={16} aria-hidden="true" />
                      Create
                    </Button>
                  </div>
                </form>
              </section>
            </section>

            <section className="routing-block">
              <section className="panel routing-list-panel">
                <div className="panel-header">
                  <PanelTitle icon={Server} title="Nodes" />
                  <Badge>{nodes.length} nodes</Badge>
                </div>
                <DataTable
                  columns={["name", "provider", "model", "api_base", "endpoints", "actions"]}
                  rows={nodes as JsonRecord[]}
                  empty="No nodes"
                  renderCell={(row, column) => renderNodeCell(row, column, loading, startNodeEdit, deleteNode)}
                />
                {routingEdit?.kind === "node" && (
                  <form className="routing-edit-form" onSubmit={(event) => void updateNode(event)}>
                    <div className="routing-edit-header">
                      <span className="resource-name">{routingEdit.form.name}</span>
                      <Button type="button" variant="ghost" size="icon" onClick={cancelRoutingEdit} title="Cancel edit">
                        <X size={16} aria-hidden="true" />
                      </Button>
                    </div>
                    <label className="field">
                      <span>Name</span>
                      <input value={routingEdit.form.name} readOnly aria-readonly="true" />
                    </label>
                    <label className="field">
                      <span>Provider</span>
                      <select
                        required
                        value={routingEdit.form.provider}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, provider: event.target.value } })}
                      >
                        <option value="">Select provider</option>
                        {providerNames.map((name) => (
                          <option key={name} value={name}>{name}</option>
                        ))}
                      </select>
                      <FieldError message={routingEdit.errors.provider} />
                    </label>
                    <label className="field">
                      <span>Upstream model</span>
                      <input
                        required
                        value={routingEdit.form.model}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, model: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.model} />
                    </label>
                    <label className="field">
                      <span>API base</span>
                      <input
                        value={routingEdit.form.api_base}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, api_base: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.api_base} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Endpoints</span>
                      <textarea
                        className="routing-list-textarea"
                        value={routingEdit.form.endpoints_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, endpoints_text: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.endpoints_text} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Limits</span>
                      <textarea
                        className="routing-list-textarea json-textarea"
                        value={routingEdit.form.limits_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, limits_text: event.target.value } })}
                        spellCheck={false}
                      />
                      <FieldError message={routingEdit.errors.limits_text} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Modules</span>
                      <textarea
                        className="routing-list-textarea json-textarea"
                        value={routingEdit.form.modules_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, modules_text: event.target.value } })}
                        spellCheck={false}
                      />
                      <FieldError message={routingEdit.errors.modules_text} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Allowed keys</span>
                      <textarea
                        className="routing-list-textarea json-textarea"
                        value={routingEdit.form.allowed_keys_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, allowed_keys_text: event.target.value } })}
                        spellCheck={false}
                      />
                      <FieldError message={routingEdit.errors.allowed_keys_text} />
                    </label>
                    <div className="form-actions">
                      <Button type="submit" disabled={loading || providerNames.length === 0}>
                        <Save size={16} aria-hidden="true" />
                        Save
                      </Button>
                    </div>
                  </form>
                )}
              </section>

              <section className="panel">
                <PanelTitle icon={Plus} title="New Node" />
                <form className="model-form routing-form" onSubmit={(event) => void createNode(event)}>
                  <label className="field">
                    <span>Name</span>
                    <input required value={nodeForm.name} onChange={(event) => setNodeForm({ ...nodeForm, name: event.target.value })} />
                    <FieldError message={nodeErrors.name} />
                  </label>
                  <label className="field">
                    <span>Provider</span>
                    <select required value={nodeForm.provider} onChange={(event) => setNodeForm({ ...nodeForm, provider: event.target.value })}>
                      <option value="">Select provider</option>
                      {providerNames.map((name) => (
                        <option key={name} value={name}>{name}</option>
                      ))}
                    </select>
                    <FieldError message={nodeErrors.provider} />
                  </label>
                  <label className="field">
                    <span>Upstream model</span>
                    <input required value={nodeForm.model} onChange={(event) => setNodeForm({ ...nodeForm, model: event.target.value })} />
                    <FieldError message={nodeErrors.model} />
                  </label>
                  <label className="field">
                    <span>API base</span>
                    <input value={nodeForm.api_base} onChange={(event) => setNodeForm({ ...nodeForm, api_base: event.target.value })} />
                    <FieldError message={nodeErrors.api_base} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Endpoints</span>
                    <textarea
                      className="routing-list-textarea"
                      value={nodeForm.endpoints_text}
                      onChange={(event) => setNodeForm({ ...nodeForm, endpoints_text: event.target.value })}
                    />
                    <FieldError message={nodeErrors.endpoints_text} />
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading || providerNames.length === 0}>
                      <Plus size={16} aria-hidden="true" />
                      Create
                    </Button>
                  </div>
                </form>
              </section>
            </section>

            <section className="routing-block">
              <section className="panel routing-list-panel">
                <div className="panel-header">
                  <PanelTitle icon={Network} title="Client Models" />
                  <Badge>{clientModels.length} models</Badge>
                </div>
                <DataTable
                  columns={["name", "nodes", "balancing", "node_weights", "actions"]}
                  rows={clientModels as JsonRecord[]}
                  empty="No client models"
                  renderCell={(row, column) => renderClientModelCell(row, column, loading, startClientModelEdit, deleteClientModel)}
                />
                {routingEdit?.kind === "client-model" && (
                  <form className="routing-edit-form" onSubmit={(event) => void updateClientModel(event)}>
                    <div className="routing-edit-header">
                      <span className="resource-name">{routingEdit.form.name}</span>
                      <Button type="button" variant="ghost" size="icon" onClick={cancelRoutingEdit} title="Cancel edit">
                        <X size={16} aria-hidden="true" />
                      </Button>
                    </div>
                    <label className="field">
                      <span>Name</span>
                      <input value={routingEdit.form.name} readOnly aria-readonly="true" />
                    </label>
                    <label className="field routing-nodes-field">
                      <span>Nodes</span>
                      <textarea
                        className="routing-list-textarea"
                        value={routingEdit.form.nodes_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, nodes_text: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.nodes_text} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Node weights</span>
                      <textarea
                        className="routing-list-textarea"
                        value={routingEdit.form.node_weights_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, node_weights_text: event.target.value } })}
                      />
                      <FieldError message={routingEdit.errors.node_weights_text} />
                    </label>
                    <label className="field">
                      <span>Balancing</span>
                      <input value={routingEdit.form.balancing} onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, balancing: event.target.value } })} />
                      <FieldError message={routingEdit.errors.balancing} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Limits</span>
                      <textarea
                        className="routing-list-textarea json-textarea"
                        value={routingEdit.form.limits_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, limits_text: event.target.value } })}
                        spellCheck={false}
                      />
                      <FieldError message={routingEdit.errors.limits_text} />
                    </label>
                    <label className="field routing-nested-field">
                      <span>Allowed keys</span>
                      <textarea
                        className="routing-list-textarea json-textarea"
                        value={routingEdit.form.allowed_keys_text}
                        onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, allowed_keys_text: event.target.value } })}
                        spellCheck={false}
                      />
                      <FieldError message={routingEdit.errors.allowed_keys_text} />
                    </label>
                    <div className="form-actions">
                      <Button type="submit" disabled={loading || nodeNames.length === 0}>
                        <Save size={16} aria-hidden="true" />
                        Save
                      </Button>
                    </div>
                  </form>
                )}
              </section>

              <section className="panel">
                <PanelTitle icon={Plus} title="New Client Model" />
                <form className="model-form routing-form" onSubmit={(event) => void createClientModel(event)}>
                  <label className="field">
                    <span>Name</span>
                    <input required value={clientModelForm.name} onChange={(event) => setClientModelForm({ ...clientModelForm, name: event.target.value })} />
                    <FieldError message={clientModelErrors.name} />
                  </label>
                  <label className="field routing-nodes-field">
                    <span>Nodes</span>
                    <div className="choice-list">
                      {nodeNames.map((name) => (
                        <label key={name} className="choice-item">
                          <input
                            type="checkbox"
                            checked={clientModelForm.nodes.includes(name)}
                            onChange={(event) => {
                              const selected = event.target.checked
                                ? [...clientModelForm.nodes, name]
                                : clientModelForm.nodes.filter((nodeName) => nodeName !== name);
                              setClientModelForm({ ...clientModelForm, nodes: selected });
                            }}
                          />
                          <span>{name}</span>
                        </label>
                      ))}
                      {nodeNames.length === 0 && <span className="empty-inline">No nodes available</span>}
                    </div>
                    <FieldError message={clientModelErrors.nodes} />
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading || nodeNames.length === 0}>
                      <Plus size={16} aria-hidden="true" />
                      Create
                    </Button>
                  </div>
                </form>
              </section>
            </section>

            <section className="routing-block">
              <section className="panel routing-list-panel">
                <div className="panel-header">
                  <PanelTitle icon={Network} title="Fallback Chains" />
                  <Badge>{routerFallbacks.length} chains</Badge>
                </div>
                <form className="fallback-editor" onSubmit={(event) => void saveRouterFallbacks(event)}>
                  <div className="fallback-list">
                    {routerFallbacks.map((row) => (
                      <div className="fallback-row" key={row.id}>
                        <label className="field">
                          <span>Source</span>
                          <select required value={row.source} onChange={(event) => updateFallbackSource(row.id, event.target.value)}>
                            <option value="">Select source</option>
                            {fallbackModelNames.map((name) => (
                              <option key={name} value={name}>{name}</option>
                            ))}
                          </select>
                        </label>
                        <label className="field fallback-targets-field">
                          <span>Targets</span>
                          <div className="choice-list fallback-choice-list">
                            {fallbackModelNames.filter((name) => name !== row.source).map((name) => (
                              <label key={name} className="choice-item">
                                <input
                                  type="checkbox"
                                  checked={row.targets.includes(name)}
                                  onChange={(event) => toggleFallbackTarget(row.id, name, event.target.checked)}
                                />
                                <span>{name}</span>
                              </label>
                            ))}
                            {fallbackModelNames.filter((name) => name !== row.source).length === 0 && <span className="empty-inline">No targets available</span>}
                          </div>
                        </label>
                        <Button type="button" variant="danger" size="icon" disabled={loading} onClick={() => removeFallbackRow(row.id)} title="Remove fallback chain">
                          <Trash2 size={16} aria-hidden="true" />
                        </Button>
                      </div>
                    ))}
                    {routerFallbacks.length === 0 && <div className="empty-inline fallback-empty">No fallback chains</div>}
                  </div>
                  <FieldError message={fallbackError} />
                  <div className="form-actions">
                    <Button type="submit" disabled={loading}>
                      <Save size={16} aria-hidden="true" />
                      Save
                    </Button>
                  </div>
                </form>
              </section>

              <section className="panel">
                <PanelTitle icon={Plus} title="New Fallback" />
                <form className="model-form routing-form" onSubmit={addFallbackRow}>
                  <label className="field">
                    <span>Source</span>
                    <select
                      required
                      value={newFallback.source}
                      onChange={(event) => {
                        const source = event.target.value;
                        setNewFallback({ source, targets: newFallback.targets.filter((target) => target !== source) });
                        setFallbackError("");
                      }}
                    >
                      <option value="">Select source</option>
                      {fallbackModelNames.map((name) => (
                        <option key={name} value={name}>{name}</option>
                      ))}
                    </select>
                  </label>
                  <label className="field routing-nodes-field">
                    <span>Targets</span>
                    <div className="choice-list">
                      {fallbackModelNames.filter((name) => name !== newFallback.source).map((name) => (
                        <label key={name} className="choice-item">
                          <input
                            type="checkbox"
                            checked={newFallback.targets.includes(name)}
                            onChange={(event) => {
                              setNewFallback({ ...newFallback, targets: toggleStringSelection(newFallback.targets, name, event.target.checked) });
                              setFallbackError("");
                            }}
                          />
                          <span>{name}</span>
                        </label>
                      ))}
                      {fallbackModelNames.filter((name) => name !== newFallback.source).length === 0 && <span className="empty-inline">No targets available</span>}
                    </div>
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading || fallbackModelNames.length < 2}>
                      <Plus size={16} aria-hidden="true" />
                      Add
                    </Button>
                  </div>
                </form>
              </section>
            </section>

            <section className="routing-block">
              <section className="panel routing-list-panel">
                <div className="panel-header">
                  <PanelTitle icon={Server} title="Model Tree" />
                  <Badge>{legacyModelNames.length} models</Badge>
                </div>
                {copyModelForm.source && (
                  <form className="routing-edit-form legacy-model-copy-form" onSubmit={copyLegacyModel}>
                    <div className="routing-edit-header">
                      <strong>Copy {copyModelForm.source}</strong>
                      <Button type="button" variant="ghost" size="icon" onClick={cancelModelCopy} title="Cancel copy">
                        <X size={16} aria-hidden="true" />
                      </Button>
                    </div>
                    <label className="field">
                      <span>Source</span>
                      <input type="text" value={copyModelForm.source} readOnly />
                    </label>
                    <label className="field">
                      <span>Target</span>
                      <input
                        type="text"
                        value={copyModelForm.target}
                        onChange={(event) => setCopyModelForm({ ...copyModelForm, target: event.target.value, error: "" })}
                        autoFocus
                      />
                    </label>
                    <FieldError message={copyModelForm.error} />
                    <div className="form-actions">
                      <Button type="submit" disabled={loading || !copyModelForm.target.trim()}>
                        <Copy size={16} aria-hidden="true" />
                        Copy
                      </Button>
                    </div>
                  </form>
                )}
                <DataTable
                  columns={["model", "parent", "children", "protected", "editable", "actions"]}
                  rows={modelTreeRows(modelTree)}
                  empty="No legacy models"
                  renderCell={(row, column) => renderModelTreeCell(row, column, loading, startModelCopy, deleteLegacyModel)}
                />
              </section>

              <section className="panel">
                <div className="panel-header">
                  <PanelTitle icon={Network} title="Dependencies" />
                  <Button variant="secondary" size="icon" disabled={loading || !dependencyModel} onClick={() => void loadModelDependencies(dependencyModel).catch((err) => setDependencyError(errorMessage(err)))} title="Reload dependencies">
                    <RefreshCw size={17} aria-hidden="true" />
                  </Button>
                </div>
                <label className="field">
                  <span>Model</span>
                  <select value={dependencyModel} onChange={(event) => setDependencyModel(event.target.value)} disabled={legacyModelNames.length === 0}>
                    <option value="">Select model</option>
                    {legacyModelNames.map((name) => (
                      <option key={name} value={name}>{name}</option>
                    ))}
                  </select>
                </label>
                <FieldError message={dependencyError} />
                <div className="dependency-summary">
                  <div className="dependency-group">
                    <span>Ancestry</span>
                    {renderStringBadges(asArray<string>(modelAncestry?.chain))}
                  </div>
                  <div className="dependency-group">
                    <span>Direct children</span>
                    {renderStringBadges(asArray<string>(modelDependents?.direct_children))}
                  </div>
                  <div className="dependency-group">
                    <span>Descendants</span>
                    {renderStringBadges(asArray<string>(modelDependents?.all_descendants))}
                  </div>
                </div>
              </section>
            </section>
          </div>
        )}

        {activeTab === "keys" && (
          <div className="keys-view">
            {secretReveal && <SecretBanner reveal={secretReveal} onDismiss={() => setSecretReveal(null)} />}

            <section className="panel wide">
              <div className="panel-header">
                <PanelTitle icon={KeyRound} title="App Keys" />
                <div className="toolbar">
                  <Badge tone={appKeysConfig.enabled ? "good" : "neutral"}>{appKeysConfig.enabled ? "enabled" : "disabled"}</Badge>
                  <Badge>{appKeys.length} keys</Badge>
                  <Button variant="secondary" size="icon" onClick={() => void loadAppKeys()} title="Reload app keys">
                    <RefreshCw size={17} aria-hidden="true" />
                  </Button>
                </div>
              </div>
              <DataTable
                columns={["key_id", "group_id", "name", "enabled", "has_secret", "models", "actions"]}
                rows={appKeys as JsonRecord[]}
                empty="No app keys"
                renderCell={(row, column) => renderAppKeyCell(row, column, loading, startAppKeyEdit, regenerateAppKey, deleteAppKey)}
              />
              {appKeyEdit && (
                <form className="routing-edit-form app-key-edit-form" onSubmit={(event) => void updateAppKey(event)}>
                  <div className="routing-edit-header">
                    <span className="resource-name">{keyIDFromRecord(appKeyEdit.original)}</span>
                    <Button type="button" variant="ghost" size="icon" onClick={cancelAppKeyEdit} title="Cancel edit">
                      <X size={16} aria-hidden="true" />
                    </Button>
                  </div>
                  <label className="field">
                    <span>Key ID</span>
                    <input value={appKeyEdit.form.key_id} readOnly aria-readonly="true" />
                  </label>
                  <label className="field">
                    <span>Group ID</span>
                    <input
                      list="app-key-groups"
                      value={appKeyEdit.form.group_id}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, group_id: event.target.value } })}
                    />
                    <FieldError message={appKeyEdit.errors.group_id} />
                  </label>
                  <label className="field">
                    <span>Name</span>
                    <input value={appKeyEdit.form.name} onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, name: event.target.value } })} />
                    <FieldError message={appKeyEdit.errors.name} />
                  </label>
                  <label className="field">
                    <span>Description</span>
                    <input value={appKeyEdit.form.description} onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, description: event.target.value } })} />
                    <FieldError message={appKeyEdit.errors.description} />
                  </label>
                  <label className="field checkbox-field">
                    <input
                      type="checkbox"
                      checked={appKeyEdit.form.enabled}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, enabled: event.target.checked } })}
                    />
                    <span>Enabled</span>
                  </label>
                  <label className="field">
                    <span>Concurrency limit</span>
                    <input
                      inputMode="numeric"
                      value={appKeyEdit.form.concurrency_limit}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, concurrency_limit: event.target.value } })}
                    />
                    <FieldError message={appKeyEdit.errors.concurrency_limit} />
                  </label>
                  <label className="field">
                    <span>Priority</span>
                    <input
                      inputMode="numeric"
                      value={appKeyEdit.form.priority}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, priority: event.target.value } })}
                    />
                    <FieldError message={appKeyEdit.errors.priority} />
                  </label>
                  <label className="field">
                    <span>Queue timeout</span>
                    <input
                      inputMode="decimal"
                      value={appKeyEdit.form.queue_timeout}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, queue_timeout: event.target.value } })}
                    />
                    <FieldError message={appKeyEdit.errors.queue_timeout} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Models</span>
                    <textarea
                      className="routing-list-textarea"
                      value={appKeyEdit.form.models_text}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, models_text: event.target.value } })}
                    />
                    <FieldError message={appKeyEdit.errors.models_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Limits JSON</span>
                    <textarea
                      className="routing-list-textarea json-textarea"
                      value={appKeyEdit.form.limits_text}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, limits_text: event.target.value } })}
                      spellCheck={false}
                    />
                    <FieldError message={appKeyEdit.errors.limits_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Required headers JSON</span>
                    <textarea
                      className="routing-list-textarea json-textarea"
                      value={appKeyEdit.form.required_headers_text}
                      onChange={(event) => setAppKeyEdit({ ...appKeyEdit, form: { ...appKeyEdit.form, required_headers_text: event.target.value } })}
                      spellCheck={false}
                    />
                    <FieldError message={appKeyEdit.errors.required_headers_text} />
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading}>
                      <Save size={16} aria-hidden="true" />
                      Save
                    </Button>
                  </div>
                </form>
              )}
            </section>

            <section className="keys-columns">
              <section className="panel">
                <PanelTitle icon={Plus} title="New App Key" />
                <form className="model-form routing-form" onSubmit={(event) => void createAppKey(event)}>
                  <label className="field">
                    <span>Key ID</span>
                    <input value={appKeyForm.key_id} onChange={(event) => setAppKeyForm({ ...appKeyForm, key_id: event.target.value })} />
                    <FieldError message={appKeyErrors.key_id} />
                  </label>
                  <label className="field">
                    <span>Group ID</span>
                    <input list="app-key-groups" value={appKeyForm.group_id} onChange={(event) => setAppKeyForm({ ...appKeyForm, group_id: event.target.value })} />
                    <datalist id="app-key-groups">
                      {appKeyGroupIDs.map((groupID) => (
                        <option key={groupID} value={groupID} />
                      ))}
                    </datalist>
                    <FieldError message={appKeyErrors.group_id} />
                  </label>
                  <label className="field">
                    <span>Name</span>
                    <input value={appKeyForm.name} onChange={(event) => setAppKeyForm({ ...appKeyForm, name: event.target.value })} />
                    <FieldError message={appKeyErrors.name} />
                  </label>
                  <label className="field">
                    <span>Description</span>
                    <input value={appKeyForm.description} onChange={(event) => setAppKeyForm({ ...appKeyForm, description: event.target.value })} />
                    <FieldError message={appKeyErrors.description} />
                  </label>
                  <label className="field checkbox-field">
                    <input type="checkbox" checked={appKeyForm.enabled} onChange={(event) => setAppKeyForm({ ...appKeyForm, enabled: event.target.checked })} />
                    <span>Enabled</span>
                  </label>
                  <label className="field">
                    <span>Concurrency limit</span>
                    <input inputMode="numeric" value={appKeyForm.concurrency_limit} onChange={(event) => setAppKeyForm({ ...appKeyForm, concurrency_limit: event.target.value })} />
                    <FieldError message={appKeyErrors.concurrency_limit} />
                  </label>
                  <label className="field">
                    <span>Priority</span>
                    <input inputMode="numeric" value={appKeyForm.priority} onChange={(event) => setAppKeyForm({ ...appKeyForm, priority: event.target.value })} />
                    <FieldError message={appKeyErrors.priority} />
                  </label>
                  <label className="field">
                    <span>Queue timeout</span>
                    <input inputMode="decimal" value={appKeyForm.queue_timeout} onChange={(event) => setAppKeyForm({ ...appKeyForm, queue_timeout: event.target.value })} />
                    <FieldError message={appKeyErrors.queue_timeout} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Models</span>
                    <textarea className="routing-list-textarea" value={appKeyForm.models_text} onChange={(event) => setAppKeyForm({ ...appKeyForm, models_text: event.target.value })} />
                    <FieldError message={appKeyErrors.models_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Limits JSON</span>
                    <textarea className="routing-list-textarea json-textarea" value={appKeyForm.limits_text} onChange={(event) => setAppKeyForm({ ...appKeyForm, limits_text: event.target.value })} spellCheck={false} />
                    <FieldError message={appKeyErrors.limits_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Required headers JSON</span>
                    <textarea className="routing-list-textarea json-textarea" value={appKeyForm.required_headers_text} onChange={(event) => setAppKeyForm({ ...appKeyForm, required_headers_text: event.target.value })} spellCheck={false} />
                    <FieldError message={appKeyErrors.required_headers_text} />
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading}>
                      <Plus size={16} aria-hidden="true" />
                      Create
                    </Button>
                  </div>
                </form>
              </section>

              <section className="panel">
                <div className="panel-header">
                  <PanelTitle icon={FileJson} title="App-Key Config" />
                  <div className="toolbar">
                    <Badge>{appKeysConfig.header_name || "x-app-key"}</Badge>
                    <Badge>{appKeyGroups(appKeysConfig).length} groups</Badge>
                  </div>
                </div>
                <form className="model-form routing-form" onSubmit={(event) => void saveAppKeysConfig(event)}>
                  <label className="field checkbox-field">
                    <input type="checkbox" checked={appKeyConfigForm.enabled} onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, enabled: event.target.checked })} />
                    <span>Enabled</span>
                  </label>
                  <label className="field checkbox-field">
                    <input
                      type="checkbox"
                      checked={appKeyConfigForm.allow_unauthenticated}
                      onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, allow_unauthenticated: event.target.checked })}
                    />
                    <span>Allow unauthenticated</span>
                  </label>
                  <label className="field">
                    <span>Header name</span>
                    <input value={appKeyConfigForm.header_name} onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, header_name: event.target.value })} />
                    <FieldError message={appKeyConfigErrors.header_name} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Defaults JSON</span>
                    <textarea className="routing-list-textarea json-textarea" value={appKeyConfigForm.defaults_text} onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, defaults_text: event.target.value })} spellCheck={false} />
                    <FieldError message={appKeyConfigErrors.defaults_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Unauthenticated JSON</span>
                    <textarea className="routing-list-textarea json-textarea" value={appKeyConfigForm.unauthenticated_text} onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, unauthenticated_text: event.target.value })} spellCheck={false} />
                    <FieldError message={appKeyConfigErrors.unauthenticated_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Groups JSON</span>
                    <textarea className="routing-list-textarea json-textarea" value={appKeyConfigForm.groups_text} onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, groups_text: event.target.value })} spellCheck={false} />
                    <FieldError message={appKeyConfigErrors.groups_text} />
                  </label>
                  <label className="field routing-nested-field">
                    <span>Required headers JSON</span>
                    <textarea className="routing-list-textarea json-textarea" value={appKeyConfigForm.required_headers_text} onChange={(event) => setAppKeyConfigForm({ ...appKeyConfigForm, required_headers_text: event.target.value })} spellCheck={false} />
                    <FieldError message={appKeyConfigErrors.required_headers_text} />
                  </label>
                  <div className="form-actions">
                    <Button type="submit" disabled={loading}>
                      <Save size={16} aria-hidden="true" />
                      Save
                    </Button>
                  </div>
                </form>
              </section>
            </section>
          </div>
        )}
      </section>
    </main>
  );
}

function Metric({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) {
	return (
		<section className="metric">
      <Icon size={19} aria-hidden="true" />
      <span>{label}</span>
      <strong>{value}</strong>
    </section>
	);
}

function ValidationIssues({ issues, summary }: { issues: ValidationIssue[]; summary: { errors: number; warnings: number } }) {
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

function FieldError({ message }: { message?: string }) {
  if (!message) return null;
  return <span className="field-error">{message}</span>;
}

function CacheStatus({ error, status, stats, loading }: { error: string; status: number | null; stats: CacheStats; loading: boolean }) {
  const message = cacheStatusMessage(error, status, stats, loading);
  if (!message) return null;
  return (
    <div className="inline-status">
      <Badge tone={cacheStatusTone(error, status, stats, loading)}>{cacheStatusLabel(error, status, stats, loading)}</Badge>
      <span>{message}</span>
    </div>
  );
}

function CatalogStatus({ error, status, snapshot, loading }: { error: string; status: number | null; snapshot: CatalogSnapshot | null; loading: boolean }) {
  const message = catalogStatusMessage(error, status, snapshot, loading);
  if (!message) return null;
  return (
    <div className="inline-status catalog-status">
      <Badge tone={catalogStatusTone(error, status, snapshot, loading)}>{catalogStatusLabel(error, status, snapshot, loading)}</Badge>
      <span>{message}</span>
    </div>
  );
}

function CatalogWarnings({ warnings }: { warnings: CatalogWarning[] }) {
  if (warnings.length === 0) {
    return (
      <div className="validation-summary">
        <Badge tone="good">No catalog warnings</Badge>
      </div>
    );
  }
  return (
    <div className="validation-list">
      <div className="validation-summary">
        <Badge tone="warn">{warnings.length} warnings</Badge>
      </div>
      {warnings.map((warning, index) => (
        <div className="validation-row catalog-hint-row" key={`${warning.code || "warning"}-${warning.model || ""}-${warning.node || ""}-${index}`}>
          <Badge tone={warning.severity === "warning" ? "warn" : "neutral"}>{warning.severity || "info"}</Badge>
          <code>{warning.code || "catalog_warning"}</code>
          <span>{warning.message || "Catalog advisory warning"}</span>
        </div>
      ))}
    </div>
  );
}

function TraceStatus({ error, status, detail, loading }: { error: string; status: number | null; detail: TraceDetailPayload | null; loading: boolean }) {
  const message = traceStatusMessage(error, status, detail, loading);
  if (!message) return null;
  return (
    <div className="inline-status trace-status">
      <Badge tone={traceStatusTone(error, status, detail, loading)}>{traceStatusLabel(error, status, detail, loading)}</Badge>
      <span>{message}</span>
    </div>
  );
}

function TraceSummaryValue({ label, value }: { label: string; value: string | JSX.Element }) {
  return (
    <div className="trace-summary-value">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function TraceTimeline({ items }: { items: TraceTimelineItem[] }) {
  if (items.length === 0) {
    return <div className="empty-inline trace-empty">No trace events returned</div>;
  }
  return (
    <div className="trace-timeline">
      {items.map((item) => (
        <div className="trace-timeline-item" key={item.id}>
          <div className="trace-marker" aria-hidden="true" />
          <div className="trace-event">
            <div className="trace-event-header">
              <div>
                <Badge tone={item.tone}>{item.phase}</Badge>
                <strong>{item.title}</strong>
              </div>
              {item.time && <span>{formatTime(item.time)}</span>}
            </div>
            <div className="trace-field-grid">
              {item.fields.map((field) => (
                <div className="trace-field" key={`${item.id}-${field.label}`}>
                  <span>{field.label}</span>
                  {field.tone ? <Badge tone={field.tone}>{formatValue(field.value)}</Badge> : <strong>{formatValue(field.value)}</strong>}
                </div>
              ))}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

function PanelTitle({ icon: Icon, title }: { icon: typeof Activity; title: string }) {
  return (
    <div className="panel-title">
      <Icon size={18} aria-hidden="true" />
      <h3>{title}</h3>
    </div>
  );
}

function DataTable({
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
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column}>{column.replace(/_/g, " ")}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={String(row.id || row.name || row.model_name || index)}>
              {columns.map((column) => (
                <td key={column}>{renderCell(row, column)}</td>
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

function SecretBanner({ reveal, onDismiss }: { reveal: SecretReveal; onDismiss: () => void }) {
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

async function requestJSON<T>(path: string, init?: RequestInit, adminPassword?: string): Promise<T> {
  const headers = new Headers(init?.headers);
  if (adminPassword) {
    headers.set("x-admin-password", adminPassword);
  }
  if (init?.body && !headers.has("content-type")) {
    headers.set("content-type", "application/json");
  }
  const response = await fetch(path, { ...init, headers });
  const text = await response.text();
  const payload = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new APIError(String(payload.error?.message || payload.message || payload.error || response.statusText), response.status, payload);
  }
  return payload as T;
}

class APIError extends Error {
  status: number;
  payload: unknown;

  constructor(message: string, status: number, payload: unknown) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.payload = payload;
  }
}

function apiErrorPayload(err: unknown) {
  return err instanceof APIError ? err.payload : undefined;
}

function validateModelForm(form: ModelForm) {
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

function validateProviderForm(form: ProviderForm) {
  const errors: Partial<Record<keyof ProviderForm, string>> = {};
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

function validateNodeForm(form: NodeForm) {
  const errors: Partial<Record<keyof NodeForm, string>> = {};
  if (!form.name.trim()) {
    errors.name = "Name is required";
  }
  if (!form.provider.trim()) {
    errors.provider = "Provider is required";
  }
  if (!form.model.trim()) {
    errors.model = "Upstream model is required";
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

function validateClientModelForm(form: ClientModelForm) {
  const errors: Partial<Record<keyof ClientModelForm, string>> = {};
  if (!form.name.trim()) {
    errors.name = "Name is required";
  }
  if (form.nodes.length === 0) {
    errors.nodes = "Choose at least one node";
  }
  return errors;
}

function validateClientModelEditForm(form: ClientModelEditForm, nodes: string[], weightsResult: ParsedNodeWeights) {
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

function validateHTTPURL(value: string) {
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

function validateRoutingEndpointsText(value: string) {
  const endpoints = parseStringList(value);
  for (const endpoint of endpoints) {
    if (!validRoutingEndpoint(endpoint)) {
      return "Use chat, embeddings, rerank, messages, responses, all, or a /v1 path";
    }
  }
  return "";
}

function validRoutingEndpoint(value: string) {
  const normalized = normalizeRoutingEndpoint(value);
  if (!normalized) return true;
  return ["chat", "embeddings", "rerank", "messages", "responses"].includes(normalized);
}

function normalizeRoutingEndpoint(value: string) {
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

function changedStringFields<T extends JsonRecord>(original: T, form: Record<string, string>, fields: string[]) {
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

function formatStringList(values: string[]) {
  return values.join("\n");
}

function parseStringList(value: string) {
  return Array.from(
    new Set(
      value
        .split(/[,\n]/)
        .map((item) => item.trim())
        .filter(Boolean)
    )
  );
}

function uniqueStrings(values: string[]) {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean)));
}

function toggleStringSelection(values: string[], value: string, checked: boolean) {
  if (checked) {
    return uniqueStrings([...values, value]);
  }
  return values.filter((item) => item !== value);
}

function nextFallbackRowID() {
  return `fallback-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function fallbackRowsFromPayload(value: unknown): FallbackRow[] {
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

function fallbackPayloadFromRows(rows: FallbackRow[]) {
  return rows.map((row) => ({ [row.source.trim()]: uniqueStrings(row.targets) }));
}

function validateFallbackRows(rows: FallbackRow[]) {
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

function fallbackNameOptions(legacyModels: string[], clientModels: ClientModelRecord[], nodes: NodeRecord[], rows: FallbackRow[]) {
  return uniqueStrings([
    ...legacyModels,
    ...clientModels.map((model) => resourceName(model)),
    ...nodes.map((node) => resourceName(node)),
    ...rows.flatMap((row) => [row.source, ...row.targets])
  ]).sort((left, right) => left.localeCompare(right));
}

function normalizeModelTreePayload(payload: ModelTreePayload): ModelTreePayload {
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

function modelTreeNames(tree: ModelTreePayload) {
  return Object.keys(tree.nodes || {}).sort((left, right) => left.localeCompare(right));
}

function adminModelPathName(name: string) {
  // The Go handler calls PathUnescape on r.URL.Path, which net/http has already unescaped once.
  return encodeURIComponent(encodeURIComponent(name));
}

function modelTreeRows(tree: ModelTreePayload): JsonRecord[] {
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

function renderModelTreeCell(
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

function ModelTreeActions({
  name,
  disabled,
  onCopy,
  onDelete
}: {
  name?: string;
  disabled: boolean;
  onCopy: (name?: string) => void;
  onDelete: (name?: string) => Promise<void>;
}) {
  return (
    <span className="action-cell">
      <Button variant="secondary" size="icon" disabled={disabled || !name} onClick={() => onCopy(name)} title={name ? `Copy ${name}` : "Copy"}>
        <Copy size={16} aria-hidden="true" />
      </Button>
      <DeleteButton name={name} disabled={disabled} onDelete={onDelete} />
    </span>
  );
}

function nextCopyModelName(source: string, existingNames: string[]) {
  const existing = new Set(existingNames);
  const base = `${source}-copy`;
  if (!existing.has(base)) return base;
  for (let index = 2; index < 1000; index += 1) {
    const candidate = `${base}-${index}`;
    if (!existing.has(candidate)) return candidate;
  }
  return `${base}-${Date.now()}`;
}

function legacyModelDependentsConflict(err: unknown) {
  if (!(err instanceof APIError) || err.status !== 409) return null;
  const payload = apiErrorPayload(err);
  if (payloadErrorCode(payload) !== "model_has_dependents") return null;
  return {
    directChildren: modelDependentsList(payload, "direct_children"),
    descendants: modelDependentsList(payload, "dependents")
  };
}

function modelDependentsList(payload: unknown, field: "direct_children" | "all_descendants" | "dependents") {
  if (!isJsonRecord(payload)) return [];
  let values = asArray<unknown>(payload[field]).filter(isNonEmptyString);
  if (values.length === 0 && isJsonRecord(payload.error)) {
    values = asArray<unknown>(payload.error[field]).filter(isNonEmptyString);
  }
  return uniqueStrings(values);
}

function protectedModelNames(tree: ModelTreePayload, names: string[]) {
  return uniqueStrings(names).filter((name) => Boolean(tree.nodes?.[name]?.protected));
}

function protectedModelPasswordMessage(names: string[]) {
  const label = names.length === 1 ? "model" : "models";
  return `Admin password required for protected ${label}: ${names.join(", ")}`;
}

function legacyModelDeleteConfirmMessage(name: string, directChildren: string[], descendants: string[], protectedNames: string[], cascade: boolean) {
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

function descendantCountLabel(count: number) {
  return `${count} descendant${count === 1 ? "" : "s"}`;
}

function formatModelList(values: string[]) {
  return values.length > 0 ? values.join(", ") : "none";
}

function parseNodeList(value: string) {
  return parseStringList(value);
}

function arraysEqual(left: string[], right: string[]) {
  if (left.length !== right.length) return false;
  return left.every((item, index) => item === right[index]);
}

type ParsedNodeWeights = {
  weights: Record<string, number>;
  error?: string;
};

function parseNodeWeights(value: string): ParsedNodeWeights {
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

function formatNodeWeights(value: unknown) {
  const weights = asIntegerRecord(value);
  return Object.keys(weights)
    .sort()
    .map((key) => `${key}=${weights[key]}`)
    .join("\n");
}

function asIntegerRecord(value: unknown) {
  if (!isJsonRecord(value)) return {};
  const out: Record<string, number> = {};
  Object.entries(value).forEach(([key, rawValue]) => {
    if (typeof rawValue === "number" && Number.isSafeInteger(rawValue)) {
      out[key] = rawValue;
    }
  });
  return out;
}

function integerRecordsEqual(left: Record<string, number>, right: Record<string, number>) {
  const leftKeys = Object.keys(left).sort();
  const rightKeys = Object.keys(right).sort();
  if (!arraysEqual(leftKeys, rightKeys)) return false;
  return leftKeys.every((key) => left[key] === right[key]);
}

function appKeyFormFromRecord(key: AppKeyRecord): AppKeyForm {
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

function appKeyConfigFormFromPayload(payload: AppKeysPayload): AppKeyConfigForm {
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

function appKeyPayloadFromForm(form: AppKeyForm, original: AppKeyRecord | null, update: boolean) {
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

function addNumericPayloadField(
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

function appKeysConfigPayloadFromForm(form: AppKeyConfigForm) {
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

function parseJSONField<T>(value: string, label: string, expected: "object" | "array"): { value: T; error?: string } {
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

function parseAllowedKeysField(value: string, label: string): { value: AllowedKeysValue; error?: string } {
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

function parseOptionalNumber(value: string, label: string, kind: "integer" | "number") {
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

function secretRevealFromResponse(response: AppKeyMutationPayload): SecretReveal {
  return {
    keyId: String(response.key_id || response.key?.key_id || ""),
    secret: String(response.secret || ""),
    message: String(response.message || "Store this secret securely - it will not be shown again")
  };
}

function renderAppKeyCell(
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

function AppKeyActions({
  row,
  keyID,
  disabled,
  onEdit,
  onRegenerate,
  onDelete
}: {
  row: AppKeyRecord;
  keyID?: string;
  disabled: boolean;
  onEdit: (row: AppKeyRecord) => void;
  onRegenerate: (keyID?: string) => Promise<void>;
  onDelete: (keyID?: string) => Promise<void>;
}) {
  return (
    <span className="action-cell">
      <Button variant="secondary" size="icon" disabled={disabled || !keyID} onClick={() => onEdit(row)} title={keyID ? `Edit ${keyID}` : "Edit"}>
        <Pencil size={16} aria-hidden="true" />
      </Button>
      <Button variant="secondary" size="icon" disabled={disabled || !keyID} onClick={() => void onRegenerate(keyID)} title={keyID ? `Regenerate ${keyID}` : "Regenerate"}>
        <RefreshCw size={16} aria-hidden="true" />
      </Button>
      <DeleteButton name={keyID} disabled={disabled} onDelete={onDelete} />
    </span>
  );
}

function appKeyGroups(payload: AppKeysPayload) {
  return asArray<JsonRecord>(payload.groups);
}

function keyIDFromRecord(row: AppKeyRecord | JsonRecord) {
  return typeof row.key_id === "string" ? row.key_id : "";
}

function formatNumberInput(value: unknown) {
  return typeof value === "number" && value !== 0 ? String(value) : "";
}

function formatJSONText(value: unknown) {
  return JSON.stringify(value, null, 2);
}

function jsonEqual(left: unknown, right: unknown) {
  return JSON.stringify(left ?? null) === JSON.stringify(right ?? null);
}

function isEmptyObject(value: JsonRecord) {
  return Object.keys(value).length === 0;
}

function renderProviderCell(
  row: JsonRecord,
  column: string,
  disabled: boolean,
  onEdit: (provider: ProviderRecord) => void,
  onDelete: (name?: string) => Promise<void>
) {
  const name = resourceName(row);
  if (column === "name") return <span className="resource-name">{name || "-"}</span>;
  if (column === "api_type") return row.api_type ? <Badge>{formatValue(row.api_type)}</Badge> : "-";
  if (column === "endpoints") return renderStringBadges(asArray<string>(row.endpoints));
  if (column === "actions") return <RoutingActions row={row as ProviderRecord} name={name} disabled={disabled} onEdit={onEdit} onDelete={onDelete} />;
  return formatValue(row[column]);
}

function renderNodeCell(
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

function renderClientModelCell(
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

function renderStringBadges(values: string[]) {
  if (values.length === 0) return "-";
  return (
    <span className="badge-stack">
      {values.map((value, index) => (
        <Badge key={`${value}-${index}`}>{value}</Badge>
      ))}
    </span>
  );
}

function RoutingActions<T extends JsonRecord>({
  row,
  name,
  disabled,
  onEdit,
  onDelete
}: {
  row: T;
  name?: string;
  disabled: boolean;
  onEdit: (row: T) => void;
  onDelete: (name?: string) => Promise<void>;
}) {
  return (
    <span className="action-cell">
      <Button variant="secondary" size="icon" disabled={disabled || !name} onClick={() => onEdit(row)} title={name ? `Edit ${name}` : "Edit"}>
        <Pencil size={16} aria-hidden="true" />
      </Button>
      <DeleteButton name={name} disabled={disabled} onDelete={onDelete} />
    </span>
  );
}

function DeleteButton({ name, disabled, onDelete }: { name?: string; disabled: boolean; onDelete: (name?: string) => Promise<void> }) {
  return (
    <>
      <Button variant="danger" size="icon" disabled={disabled || !name} onClick={() => void onDelete(name)} title={name ? `Delete ${name}` : "Delete"}>
        <Trash2 size={16} aria-hidden="true" />
      </Button>
    </>
  );
}

function deleteConflictMessage(err: unknown, label: string, name: string) {
  if (err instanceof APIError && err.status === 409) {
    const payload = apiErrorPayload(err);
    const code = payloadErrorCode(payload);
    const references = payloadReferences(payload);
    const referenceText = references.length > 0 ? `: ${references.join(", ")}` : "";
    if (code === "provider_has_nodes") {
      return `Provider "${name}" is still referenced by nodes${referenceText}`;
    }
    if (code === "node_has_client_models") {
      return `Node "${name}" is still referenced by client models${referenceText}`;
    }
    return `${titleCase(label)} "${name}" is still referenced${referenceText}`;
  }
  return errorMessage(err);
}

function payloadErrorCode(payload: unknown) {
  if (!isJsonRecord(payload) || !isJsonRecord(payload.error)) return "";
  return typeof payload.error.code === "string" ? payload.error.code : "";
}

function payloadReferences(payload: unknown) {
  if (!isJsonRecord(payload)) return [];
  return asArray<unknown>(payload.references).filter(isNonEmptyString);
}

function resourceName(row: JsonRecord) {
  if (typeof row.name === "string") return row.name;
  if (typeof row.model_name === "string") return row.model_name;
  return "";
}

function titleCase(value: string) {
  return value.replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function isJsonRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

function asArray<T>(value: unknown): T[] {
  return Array.isArray(value) ? (value as T[]) : [];
}

function numberValue(value: unknown, fallback = 0) {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function hasCacheSnapshot(stats: CacheStats) {
  return Object.keys(stats).length > 0;
}

function cacheHitRate(stats: CacheStats) {
  const hits = numberValue(stats.hits);
  const misses = numberValue(stats.misses);
  const total = hits + misses;
  return total > 0 ? hits / total : 0;
}

function cacheEntriesValue(stats: CacheStats) {
  if (!hasCacheSnapshot(stats)) return "-";
  const entries = formatValue(stats.entries ?? 0);
  return typeof stats.max_entries === "number" && stats.max_entries > 0 ? `${entries}/${stats.max_entries}` : entries;
}

function formatBytes(value: unknown) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "-";
  if (value < 1024) return `${value} B`;
  const kib = value / 1024;
  if (kib < 1024) return `${kib.toFixed(1)} KiB`;
  const mib = kib / 1024;
  if (mib < 1024) return `${mib.toFixed(1)} MiB`;
  return `${(mib / 1024).toFixed(1)} GiB`;
}

function cacheSnapshotRows(stats: CacheStats): JsonRecord[] {
  if (!hasCacheSnapshot(stats)) return [];
  const rows: JsonRecord[] = [
    { id: "mode", field: "mode", value: cacheModeLabel(stats.mode) },
    { id: "enabled", field: "enabled", value: typeof stats.enabled === "boolean" ? (stats.enabled ? "yes" : "no") : "unknown" },
    { id: "hits", field: "hits", value: stats.hits ?? 0 },
    { id: "misses", field: "misses", value: stats.misses ?? 0 },
    { id: "writes", field: "writes", value: stats.writes ?? 0 },
    { id: "evictions", field: "evictions", value: stats.evictions ?? 0 },
    { id: "entries", field: "entries", value: stats.entries ?? 0 },
    { id: "total_entry_bytes", field: "total entry bytes", value: stats.total_entry_bytes, format: "bytes" },
    { id: "max_entries", field: "max entries", value: stats.max_entries },
    { id: "max_entry_bytes", field: "max entry bytes", value: stats.max_entry_bytes, format: "bytes" }
  ];
  if (stats.semantic) {
    rows.push(
      { id: "semantic_enabled", field: "semantic enabled", value: typeof stats.semantic.enabled === "boolean" ? (stats.semantic.enabled ? "yes" : "no") : "unknown" },
      { id: "semantic_mode", field: "semantic mode", value: cacheModeLabel(stats.semantic.mode) },
      { id: "semantic_hits", field: "semantic hits", value: stats.semantic.hits ?? 0 },
      { id: "semantic_misses", field: "semantic misses", value: stats.semantic.misses ?? 0 },
      { id: "semantic_writes", field: "semantic writes", value: stats.semantic.writes ?? 0 },
      { id: "semantic_evictions", field: "semantic evictions", value: stats.semantic.evictions ?? 0 },
      { id: "semantic_embedding_errors", field: "semantic embedding errors", value: stats.semantic.embedding_errors ?? 0 },
      { id: "semantic_threshold_rejected", field: "semantic threshold rejected", value: stats.semantic.threshold_rejected ?? 0 },
      { id: "semantic_entries", field: "semantic entries", value: stats.semantic.entries ?? 0 },
      { id: "semantic_total_entry_bytes", field: "semantic total entry bytes", value: stats.semantic.total_entry_bytes, format: "bytes" },
      { id: "semantic_max_entries", field: "semantic max entries", value: stats.semantic.max_entries },
      { id: "semantic_max_entry_bytes", field: "semantic max entry bytes", value: stats.semantic.max_entry_bytes, format: "bytes" },
      { id: "semantic_top_k", field: "semantic top k", value: stats.semantic.top_k },
      { id: "semantic_similarity_threshold", field: "semantic similarity threshold", value: stats.semantic.similarity_threshold }
    );
  }
  return rows;
}

function renderCacheValue(row: JsonRecord) {
  return row.format === "bytes" ? formatBytes(row.value) : formatValue(row.value);
}

function catalogSnapshotRows(snapshot: CatalogSnapshot | null): CatalogRow[] {
  if (!snapshot?.enabled) return [];
  const rows: CatalogRow[] = [];
  for (const model of asArray<CatalogModelEntry>(snapshot.models)) {
    const modelName = model.model || "-";
    for (const provider of asArray<CatalogProviderEntry>(model.providers)) {
      const providerName = provider.provider || "-";
      for (const node of asArray<CatalogNodeEntry>(provider.nodes)) {
        rows.push({
          model: modelName,
          provider: providerName,
          node: node.node || "-",
          status: node.status || "unknown",
          source: node.source || "-",
          error_class: node.error_class || "",
          configured_model: node.configured_model || "",
          target_model: node.target_model || "",
          last_sync: node.last_sync,
          next_sync: node.next_sync
        });
      }
    }
  }
  return rows;
}

function renderCatalogCell(row: CatalogRow, column: string) {
  if (column === "model") return <span className="resource-name">{row.model}</span>;
  if (column === "provider" || column === "node") return formatValue(row[column]);
  if (column === "status") return <Badge tone={catalogNodeStatusTone(row.status, row.source)}>{row.status}</Badge>;
  if (column === "source") return <Badge tone={row.source === "probe" ? "good" : "neutral"}>{row.source || "-"}</Badge>;
  if (column === "error_class") return row.error_class ? <Badge tone={catalogErrorClassTone(row.error_class)}>{row.error_class}</Badge> : "-";
  if (column === "last_sync" || column === "next_sync") return formatTime(row[column]);
  return formatValue(row[column]);
}

function catalogNodeStatusTone(status: string, source: string): BadgeTone {
  if (status === "available") return "good";
  if (status === "stale") return "warn";
  if (status === "unavailable") return "bad";
  if (status === "configured" && source === "probe") return "warn";
  return "neutral";
}

function catalogErrorClassTone(errorClass: string): BadgeTone {
  if (errorClass === "auth" || errorClass === "status_error" || errorClass === "invalid_response" || errorClass === "unsupported_api_type") return "bad";
  if (errorClass === "rate_limited" || errorClass === "timeout" || errorClass === "network") return "warn";
  return "neutral";
}

function catalogSafeErrorMessage(err: unknown) {
  if (err instanceof APIError) {
    if (err.status === 401 || err.status === 403) return "Admin password is required or invalid for /admin/catalog.";
    if (err.status === 404) return "This backend does not expose /admin/catalog.";
    return "Catalog snapshot is unavailable.";
  }
  return "Catalog snapshot is unavailable.";
}

function catalogStatusMessage(error: string, status: number | null, snapshot: CatalogSnapshot | null, loading: boolean) {
  if (loading && !snapshot) return "Loading catalog snapshot.";
  if (error) return error;
  if (!snapshot) return "No catalog snapshot loaded.";
  if (snapshot.enabled === false) return "Model catalog is disabled; configured routes and probes are not shown here.";
  if (snapshot.enabled === true && snapshot.probe_enabled === false) return "Catalog is enabled, but no nodes or providers are opted into probes.";
  return "";
}

function catalogStatusLabel(error: string, status: number | null, snapshot: CatalogSnapshot | null, loading: boolean) {
  if (loading && !snapshot) return "loading";
  if (error) {
    if (status === 401 || status === 403) return "auth";
    if (status === 404) return "missing";
    return "error";
  }
  if (!snapshot) return "empty";
  if (snapshot.enabled === false) return "disabled";
  if (snapshot.probe_enabled === false) return "probe off";
  return "ready";
}

function catalogStatusTone(error: string, status: number | null, snapshot: CatalogSnapshot | null, loading: boolean): BadgeTone {
  if (loading && !snapshot) return "warn";
  if (error) return status === 404 ? "warn" : "bad";
  if (!snapshot) return "neutral";
  if (snapshot.enabled === false) return "neutral";
  if (snapshot.probe_enabled === false) return "warn";
  return "good";
}

function catalogTableEmpty(snapshot: CatalogSnapshot | null, error: string, status: number | null, loading: boolean) {
  if (loading) return "Loading catalog";
  if (error && (status === 401 || status === 403)) return "Admin password required for catalog";
  if (error && status === 404) return "Catalog endpoint not available";
  if (error) return "Catalog unavailable";
  if (snapshot?.enabled === false) return "Catalog disabled";
  return "No catalog models";
}

function traceRequestsFromPayload(detail: TraceDetailPayload | null) {
  if (!detail) return [];
  const requests = firstTraceArray<TraceRequestRecord>(detail.requests, detail.logs, detail.data);
  return requests;
}

function traceAttemptsFromPayload(detail: TraceDetailPayload | null) {
  if (!detail) return [];
  return firstTraceArray<TraceAttemptRecord>(detail.attempts, detail.attempt_trail);
}

function traceErrorsFromPayload(detail: TraceDetailPayload | null) {
  if (!detail) return [];
  return firstTraceArray<TraceErrorRecord>(detail.errors, detail.error_logs);
}

function firstTraceArray<T>(...values: Array<T[] | undefined>) {
  return values.find((value) => Array.isArray(value) && value.length > 0) || [];
}

function traceTimelineItems(detail: TraceDetailPayload | null) {
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

function groupTraceRecords<T>(records: T[], keyForRecord: (record: T) => string | undefined) {
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

function traceRequestTimelineItem(request: TraceRequestRecord, index: number): TraceTimelineItem {
  const endpointKind = endpointKindFromPath(request.path) || endpointKindFromPath(request.request_path);
  const method = traceMethodLabel(request.method);
  const outcome = safeTraceCategory(request.outcome);
  return {
    id: `trace-request-${traceRequestID(request) || index}`,
    phase: "request",
    title: `${method} ${endpointKind || "request"}`,
    tone: traceOutcomeTone(outcome, request.backend_status ?? request.status),
    time: request.request_time || request.created_at,
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

function traceAttemptTimelineItem(attempt: TraceAttemptRecord, index: number): TraceTimelineItem {
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

function traceErrorTimelineItem(errorRecord: TraceErrorRecord, index: number): TraceTimelineItem {
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

function traceFields(entries: Array<[string, unknown, BadgeTone?]>): TraceTimelineField[] {
  return entries
    .filter(([, value]) => value !== undefined && value !== null && value !== "")
    .map(([label, value, tone]) => ({ label, value, tone }));
}

function traceRequestID(request: TraceRequestRecord) {
  return typeof request.id === "string" ? request.id : "";
}

function traceMethodLabel(value: unknown) {
  if (typeof value !== "string") return "REQUEST";
  const trimmed = value.trim();
  const method = trimmed.toUpperCase();
  return /^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$/.test(method) ? method : "REQUEST";
}

function safeTraceCategory(value: unknown) {
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  if (!trimmed) return "";
  if (trimmed.length > 80 || /^https?:\/\//i.test(trimmed) || /[\s/?#&=]/.test(trimmed)) return "present";
  return /^[A-Za-z0-9_.-]+$/.test(trimmed) ? trimmed : "present";
}

function endpointKindFromPath(value: unknown) {
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

function traceTokenSummary(request: TraceRequestRecord) {
  const parts = [
    tokenPart("prompt", request.prompt_tokens),
    tokenPart("completion", request.completion_tokens),
    tokenPart("cached", request.cached_tokens),
    tokenPart("reasoning", request.reasoning_tokens),
    tokenPart("total", request.total_tokens)
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" / ") : "";
}

function tokenPart(label: string, value: unknown) {
  return typeof value === "number" && value > 0 ? `${label} ${value}` : "";
}

function renderTraceAttemptCell(row: TraceAttemptRecord, column: string) {
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

function renderTraceErrorCell(row: TraceErrorRecord, column: string) {
  if (column === "timestamp" || column === "created_at") return formatTime(row[column]);
  if (column === "error_type") {
    const errorType = safeTraceCategory(row.error_type);
    return errorType ? <Badge tone="bad">{errorType}</Badge> : "-";
  }
  if (column === "http_status") return row.http_status ? <Badge tone={httpStatusTone(row.http_status)}>{row.http_status}</Badge> : "-";
  return formatValue(row[column as keyof TraceErrorRecord]);
}

function renderTraceOutcomeBadges(values: string[]) {
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

function traceSafeErrorMessage(err: unknown) {
  if (err instanceof APIError) {
    if (err.status === 401 || err.status === 403) return "Admin password is required or invalid for trace navigation.";
    if (err.status === 400) return "Trace ID is invalid.";
    if (err.status === 404) return "Trace was not found.";
    return "Trace details are unavailable.";
  }
  return "Trace details are unavailable.";
}

function traceStatusMessage(error: string, status: number | null, detail: TraceDetailPayload | null, loading: boolean) {
  if (loading && !detail) return "Loading trace.";
  if (error) return error;
  if (!detail) return "No trace loaded.";
  if (detail.has_more) return "Trace has more records than returned.";
  return "";
}

function traceStatusLabel(error: string, status: number | null, detail: TraceDetailPayload | null, loading: boolean) {
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

function traceStatusTone(error: string, status: number | null, detail: TraceDetailPayload | null, loading: boolean): BadgeTone {
  if (loading && !detail) return "warn";
  if (error) return status === 404 ? "warn" : "bad";
  if (!detail) return "neutral";
  if (detail.has_more) return "warn";
  return "good";
}

function numberTone(value: unknown, nonZeroTone: BadgeTone = "warn") {
  return typeof value === "number" && value > 0 ? nonZeroTone : undefined;
}

function httpStatusTone(status: unknown): BadgeTone {
  if (typeof status !== "number") return "neutral";
  if (status >= 500) return "bad";
  if (status >= 400) return "warn";
  if (status >= 200) return "good";
  return "neutral";
}

function traceOutcomeTone(outcome?: unknown, status?: unknown): BadgeTone {
  if (outcome === "success") return "good";
  if (outcome === "error" || outcome === "failed") return "bad";
  if (outcome === "warning" || outcome === "retry") return "warn";
  return httpStatusTone(status);
}

function normalizedCacheMode(mode?: string) {
  return (mode || "").trim().toLowerCase();
}

function cacheModeLabel(mode?: string) {
  const normalized = normalizedCacheMode(mode);
  return normalized || "unknown";
}

function cacheModeTone(mode?: string): BadgeTone {
  const normalized = normalizedCacheMode(mode);
  if (normalized === "read_write") return "good";
  if (normalized === "read_only" || normalized === "write_only") return "warn";
  return "neutral";
}

function cacheEnabledLabel(stats: CacheStats) {
  if (!hasCacheSnapshot(stats)) return "unknown";
  if (stats.enabled === true) return "enabled";
  if (stats.enabled === false) return "disabled";
  return "unknown";
}

function cacheEnabledTone(stats: CacheStats): BadgeTone {
  if (!hasCacheSnapshot(stats)) return "neutral";
  return stats.enabled === true ? "good" : "neutral";
}

function cacheStatusMessage(error: string, status: number | null, stats: CacheStats, loading: boolean) {
  if (loading && !hasCacheSnapshot(stats)) return "Loading cache counters.";
  if (error) {
    if (status === 401 || status === 403) return "Admin password is required or invalid for /admin/cache.";
    if (status === 404) return "This backend does not expose /admin/cache.";
    return error;
  }
  if (!hasCacheSnapshot(stats)) return "No cache snapshot loaded.";
  const mode = normalizedCacheMode(stats.mode);
  if (stats.enabled === false || mode === "off" || mode === "disabled") {
    return "Exact inference cache is disabled; no prompt or response content is exposed here.";
  }
  return "";
}

function cacheStatusLabel(error: string, status: number | null, stats: CacheStats, loading: boolean) {
  if (loading && !hasCacheSnapshot(stats)) return "loading";
  if (error) {
    if (status === 401 || status === 403) return "auth";
    if (status === 404) return "missing";
    return "error";
  }
  if (!hasCacheSnapshot(stats)) return "empty";
  return "disabled";
}

function cacheStatusTone(error: string, status: number | null, stats: CacheStats, loading: boolean): BadgeTone {
  if (loading && !hasCacheSnapshot(stats)) return "warn";
  if (error) return status === 404 ? "warn" : "bad";
  if (!hasCacheSnapshot(stats)) return "neutral";
  return "neutral";
}

function cacheTableEmpty(error: string, status: number | null, loading: boolean) {
  if (loading) return "Loading cache counters";
  if (error && (status === 401 || status === 403)) return "Admin password required for cache counters";
  if (error && status === 404) return "Cache endpoint not available";
  if (error) return "Cache counters unavailable";
  return "No cache snapshot";
}

function traceIDFromRecord(value: unknown) {
  if (!isJsonRecord(value)) return "";
  const candidates = [value.trace_id, value.traceId, value.traceID, value.request_trace_id];
  const match = candidates.find((candidate) => typeof candidate === "string" && candidate.trim().length > 0);
  return typeof match === "string" ? match.trim() : "";
}

function extractTraceID(payloads: unknown[]) {
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

function renderTraceID(value: string, onOpen?: (traceID: string) => Promise<void>) {
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

function quotaScopes(usage: JsonRecord): JsonRecord[] {
  const quota = usage.quota as JsonRecord | undefined;
  const used = quota?.used_by_scope as JsonRecord | undefined;
  if (!used) return [];
  return Object.entries(used).map(([scope, value], index) => ({ scope: quotaScopeLabel(scope, index), used: value }));
}

function quotaScopeLabel(scope: string, index: number) {
  if (scope === "__unauthenticated__") return "unauthenticated";
  return `quota scope ${index + 1}`;
}

function renderHealthCell(row: JsonRecord, column: string) {
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

function renderBooleanBadge(value: unknown) {
  return <Badge tone={value ? "good" : "neutral"}>{value ? "yes" : "no"}</Badge>;
}

function outcomeTone(row: LogRecord) {
  if (row.outcome === "success" || (row.backend_status && row.backend_status < 400)) return "good";
  if (row.outcome === "error" || (row.backend_status && row.backend_status >= 500)) return "bad";
  if (row.backend_status && row.backend_status >= 400) return "warn";
  return "neutral";
}

function tabFromLocation(location: Location): TabID {
  const tabParam = new URLSearchParams(location.search).get("tab");
  if (tabParam === "observability" || tabParam === "traces" || tabParam === "catalog" || tabParam === "logs" || tabParam === "config" || tabParam === "models" || tabParam === "routing" || tabParam === "keys") return tabParam;
  if (traceIDFromLocation(location)) return "traces";
  const segment = location.pathname.replace(/\/+$/, "").split("/").pop();
  if (segment === "observability" || segment === "traces" || segment === "catalog" || segment === "logs" || segment === "config" || segment === "models" || segment === "routing" || segment === "keys") return segment;
  return "overview";
}

function pathForTab(tab: TabID) {
  if (tab === "overview") return "/admin";
  if (tab === "models") return "/admin/models";
  return `/admin?tab=${tab}`;
}

function pathForTrace(traceID: string) {
  return `/admin?tab=traces&trace_id=${encodeURIComponent(traceID)}`;
}

function traceIDFromLocation(location: Location) {
  const params = new URLSearchParams(location.search);
  const fromQuery = (params.get("trace_id") || params.get("trace") || "").trim();
  if (fromQuery) return fromQuery;
  const match = location.pathname.match(/\/admin\/traces\/([^/?#]+)/);
  return match ? decodeURIComponent(match[1]).trim() : "";
}

function statusLine(backends: number, healthy: number, totalLogs: number) {
  return `${healthy}/${backends} backends healthy | ${totalLogs} log rows`;
}

function formatValue(value: unknown) {
  if (value === undefined || value === null || value === "") return "-";
  if (typeof value === "number") return Number.isInteger(value) ? value.toString() : value.toFixed(3);
  if (typeof value === "boolean") return value ? "true" : "false";
  if (typeof value === "object") return compactJSON(value);
  return String(value);
}

function compactJSON(value: unknown) {
  if (value === undefined || value === null) return "-";
  return JSON.stringify(value);
}

function percentValue(value: unknown) {
  if (typeof value !== "number") return formatValue(value);
  const normalized = value <= 1 ? value * 100 : value;
  return `${normalized.toFixed(1)}%`;
}

function formatDuration(value?: number) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "-";
  return `${value}ms`;
}

function formatTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : String(err);
}
