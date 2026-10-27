import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { adminPasswordButtonLabel, adminPasswordStatusClass, adminPasswordStatusText, adminPasswordVerificationMessage, APIError, apiErrorPayload, deleteConflictMessage, errorMessage, isAdminPasswordAuthError, requestJSON } from "../lib/api";
import { appKeyConfigFormFromPayload, appKeyFormFromRecord, appKeyGroups, appKeyPayloadFromForm, appKeysConfigPayloadFromForm, keyIDFromRecord, parseAllowedKeysField, secretRevealFromResponse } from "../lib/appKeys";
import { adminPasswordStorageKey, emptyAppKeyConfigForm, emptyAppKeyForm, emptyCopyModelForm, emptyLogFilters, logPageSize, readStoredAdminPassword } from "../lib/constants";
import { arraysEqual, asArray, formatJSONText, formatStringList, isJsonRecord, isNonEmptyString, jsonEqual, parseJSONField, parseStringList, stringField, titleCase, toggleStringSelection, uniqueStrings } from "../lib/format";
import { dateTimeFilterValue, quotaScopes } from "../lib/logs";
import { pathForTab, pathForTrace, tabFromLocation, traceIDFromLocation } from "../lib/navigation";
import { cacheSnapshotRows, catalogSafeErrorMessage, catalogSnapshotRows } from "../lib/observability";
import { adminModelPathName, asIntegerRecord, changedStringFields, descendantCountLabel, fallbackNameOptions, fallbackPayloadFromRows, fallbackRowsFromPayload, formatNodeWeights, formatProviderModels, integerRecordsEqual, legacyModelDeleteConfirmMessage, legacyModelDependentsConflict, modelDependentsList, modelTreeNames, nextCopyModelName, nextFallbackRowID, normalizeModelTreePayload, parseNodeList, parseNodeWeights, parseProviderModels, protectedModelNames, protectedModelPasswordMessage, resourceName, validateClientModelEditForm, validateClientModelForm, validateFallbackRows, validateModelForm, validateNodeForm, validateProviderForm } from "../lib/routing";
import { extractTraceID, traceAttemptsFromPayload, traceErrorsFromPayload, traceIDFromRecord, traceRequestsFromPayload, traceSafeErrorMessage, traceTimelineItems } from "../lib/traces";
import type { AdminPasswordFeedback, AppKeyConfigForm, AppKeyEditState, AppKeyForm, AppKeyMutationPayload, AppKeyRecord, AppKeysPayload, CacheStats, CatalogSnapshot, CatalogWarning, ClientModelForm, ClientModelRecord, CopyModelForm, FallbackForm, FallbackRow, JsonRecord, LimitsRecord, LogFilters, LogMeta, LogRecord, LogsPayload, ModelAncestryPayload, ModelDependentsPayload, ModelForm, ModelRecord, ModelsPayload, ModelTreePayload, NodeForm, NodeRecord, ProviderForm, ProviderRecord, RouterSettingsPayload, RoutingEditState, RoutingPayloads, SecretReveal, TabID, TraceDetailPayload, ValidationIssue, ValidationPayload } from "../types";

// Owns all admin console state, data loading, and mutations. Tabs read it
// through AdminContext rather than prop drilling.
export function useAdminController() {
  const [activeTab, setActiveTab] = useState<TabID>(() => tabFromLocation(window.location));
  const [adminPassword, setAdminPassword] = useState(readStoredAdminPassword);
  const [adminPasswordInput, setAdminPasswordInput] = useState(readStoredAdminPassword);
  const [adminPasswordVerified, setAdminPasswordVerified] = useState(false);
  const [adminPasswordChecking, setAdminPasswordChecking] = useState(false);
  const [adminPasswordPopoverOpen, setAdminPasswordPopoverOpen] = useState(false);
  const [adminPasswordFeedback, setAdminPasswordFeedback] = useState<AdminPasswordFeedback | null>(null);
  const adminPasswordControlRef = useRef<HTMLDivElement | null>(null);
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
  const [logMeta, setLogMeta] = useState<LogMeta>({ total: 0, limit: logPageSize, offset: 0, hasMore: false });
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsError, setLogsError] = useState("");
  const [selectedLog, setSelectedLog] = useState<JsonRecord | null>(null);
  const [selectedLogLoading, setSelectedLogLoading] = useState(false);
  const [selectedLogError, setSelectedLogError] = useState("");
  const [filters, setFilters] = useState<LogFilters>(emptyLogFilters);
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
  const [routingPools, setRoutingPools] = useState<JsonRecord[]>([]);
  const [prefixAffinity, setPrefixAffinity] = useState(true);
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
    models_text: "",
    name: "",
    api_base: "",
    api_type: "openai",
    api_key: "",
    endpoints_text: "",
    limits_text: "{}"
  });
  const [nodeForm, setNodeForm] = useState<NodeForm>({
    upstream_model: "",
    api_key: "",
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
  const logStopReasonOptions = useMemo(() => uniqueStrings([
    "stop", "tool_calls", "length", "content_filter", "function_call",
    ...stopReasons.map((row) => stringField(row, "reason")).filter(isNonEmptyString)
  ]), [stopReasons]);
  const selectedLogID = useMemo(() => stringField(selectedLog, "id"), [selectedLog]);
  const selectedLogIndex = useMemo(() => logs.findIndex((row) => row.id === selectedLogID), [logs, selectedLogID]);
  const providerNames = useMemo(() => providers.map((provider) => provider.name).filter(isNonEmptyString), [providers]);
  const nodeNames = useMemo(() => nodes.map((node) => node.name).filter(isNonEmptyString), [nodes]);
  const legacyModelNames = useMemo(() => modelTreeNames(modelTree), [modelTree]);
  const fallbackModelNames = useMemo(() => uniqueStrings([...fallbackNameOptions(legacyModelNames, clientModels, nodes, routerFallbacks), ...routingPools.map((pool) => String(pool.name))]), [legacyModelNames, clientModels, nodes, routerFallbacks, routingPools]);
  const appKeyGroupIDs = useMemo(() => appKeyGroups(appKeysConfig).map((group) => String(group.group_id || "")).filter(isNonEmptyString), [appKeysConfig]);
  const adminPasswordPending = adminPasswordInput !== adminPassword;
  const adminPasswordStatus = adminPasswordStatusText(adminPasswordInput, adminPassword, adminPasswordVerified, adminPasswordChecking);
  const adminPasswordStatusTone = adminPasswordStatusClass(adminPasswordInput, adminPassword, adminPasswordVerified, adminPasswordChecking);
  const adminPasswordButtonText = adminPasswordButtonLabel(adminPasswordStatus);
  const adminPasswordSubmitDisabled = adminPasswordChecking || (!adminPasswordInput && !adminPassword) || (!adminPasswordPending && adminPasswordVerified);

  useEffect(() => {
    try {
      if (adminPassword) {
        sessionStorage.setItem(adminPasswordStorageKey, adminPassword);
      } else {
        sessionStorage.removeItem(adminPasswordStorageKey);
      }
    } catch {
      // Ignore storage errors; the in-memory password still works for this tab.
    }
  }, [adminPassword]);

  useEffect(() => {
    // Success notices are transient; errors stay until the next action.
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 4000);
    return () => window.clearTimeout(timer);
  }, [notice]);

  const previousAdminPassword = useRef(adminPassword);
  useEffect(() => {
    if (previousAdminPassword.current === adminPassword) return;
    previousAdminPassword.current = adminPassword;
    if (adminPassword) void refreshAll();
  }, [adminPassword]);

  useEffect(() => {
    void refreshAll();
    if (activeTab === "traces" && traceInput.trim()) {
      void loadTraceDashboard(traceInput);
    }
  }, []);

  useEffect(() => {
    if (activeTab === "observability") {
      void loadObservability().catch(reportLoadError);
    }
    if (activeTab === "catalog") {
      void loadCatalog();
    }
    if (activeTab === "routing") {
      void loadRouting().catch(reportLoadError);
    }
    if (activeTab === "keys") {
      void loadAppKeys().catch(reportLoadError);
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

  useEffect(() => {
    if (!adminPasswordPopoverOpen) return;
    function handlePointerDown(event: PointerEvent) {
      if (adminPasswordControlRef.current?.contains(event.target as Node)) return;
      setAdminPasswordPopoverOpen(false);
    }
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setAdminPasswordPopoverOpen(false);
    }
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [adminPasswordPopoverOpen]);

  function switchTab(tab: TabID) {
    setActiveTab(tab);
    setSecretReveal(null);
    const nextPath = pathForTab(tab);
    if (`${window.location.pathname}${window.location.search}` !== nextPath) {
      window.history.pushState(null, "", nextPath);
    }
  }

  // While no admin password is set, admin APIs answer 401/403; the header's
  // "Admin locked" control already says so, so loads do not repeat it as an error.
  function isLockedAuthError(err: unknown) {
    return !adminPassword && isAdminPasswordAuthError(err);
  }

  function reportLoadError(err: unknown) {
    if (!isLockedAuthError(err)) setError(errorMessage(err));
  }

  async function refreshAll() {
    setLoading(true);
    setError("");
    setRoutingConflict("");
    try {
      const results = await Promise.allSettled([loadOverview(), loadObservability(), loadCatalog(), loadLogs(), loadConfig(), loadRouting(), loadAppKeys()]);
      const failure = results.find((result): result is PromiseRejectedResult => result.status === "rejected" && !isLockedAuthError(result.reason));
      if (failure) {
        setError(errorMessage(failure.reason));
      } else if (results.every((result) => result.status === "fulfilled")) {
        setNotice("Refreshed");
      }
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

  async function loadLogs(offset = 0, nextFilters = filters) {
    setLogsLoading(true);
    setLogsError("");
    const params = new URLSearchParams({ limit: String(logPageSize), offset: String(Math.max(0, offset)) });
    if (nextFilters.model.trim()) params.set("model", nextFilters.model.trim());
    if (nextFilters.outcome) params.set("outcome", nextFilters.outcome);
    if (nextFilters.stopReason) params.set("stop_reason", nextFilters.stopReason);
    if (nextFilters.toolCall) params.set("is_tool_call", nextFilters.toolCall);
    if (nextFilters.startDate) params.set("start_date", dateTimeFilterValue(nextFilters.startDate));
    if (nextFilters.endDate) params.set("end_date", dateTimeFilterValue(nextFilters.endDate));
    if (nextFilters.search.trim()) params.set("search", nextFilters.search.trim());
    try {
      const payload = await requestJSON<LogsPayload>(`/admin/logs?${params.toString()}`, undefined, adminPassword);
      const rows = payload.logs || payload.data || [];
      const nextOffset = Number(payload.offset ?? offset);
      const nextLimit = Number(payload.limit ?? logPageSize);
      setLogs(rows);
      setLogMeta({
        total: Number(payload.total ?? rows.length),
        limit: nextLimit > 0 ? nextLimit : logPageSize,
        offset: nextOffset > 0 ? nextOffset : 0,
        hasMore: Boolean(payload.has_more)
      });
      if (selectedLogID && !rows.some((row) => row.id === selectedLogID)) {
        setSelectedLog(null);
      }
    } catch (err) {
      setLogsError(errorMessage(err));
    } finally {
      setLogsLoading(false);
    }
  }

  function submitLogFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void loadLogs(0);
  }

  function clearLogFilters() {
    const cleared = { ...emptyLogFilters };
    setFilters(cleared);
    void loadLogs(0, cleared);
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
    const [providersPayload, nodesPayload, clientModelsPayload, routerSettingsPayload, treePayload, poolsPayload] = await Promise.all([
      requestJSON<RoutingPayloads>("/admin/providers", undefined, adminPassword),
      requestJSON<RoutingPayloads>("/admin/nodes", undefined, adminPassword),
      requestJSON<RoutingPayloads>("/admin/client-models", undefined, adminPassword),
      requestJSON<RouterSettingsPayload>("/admin/router-settings", undefined, adminPassword),
      requestJSON<ModelTreePayload>("/admin/models/tree", undefined, adminPassword).catch((): ModelTreePayload => ({ roots: [], nodes: {} })),
      requestJSON<{pools: JsonRecord[]; prefix_cache_affinity: boolean}>("/admin/routing-pools", undefined, adminPassword)
    ]);
    setRoutingPools(poolsPayload.pools);
    setPrefixAffinity(poolsPayload.prefix_cache_affinity);
    setProviders(asArray<ProviderRecord>(providersPayload.providers));
    setNodes(asArray<NodeRecord>(nodesPayload.nodes));
    setClientModels(asArray<ClientModelRecord>(clientModelsPayload.client_models));
    setRouterFallbacks(fallbackRowsFromPayload(routerSettingsPayload.fallbacks));
    setFallbackError("");
    const normalizedTree = normalizeModelTreePayload(treePayload);
    setModelTree(normalizedTree);
    setDependencyModel((current) => current || modelTreeNames(normalizedTree)[0] || "");
  }

  function resetAppKeyState() {
    setAppKeys([]);
    setAppKeysConfig({});
    setAppKeyConfigForm(emptyAppKeyConfigForm);
    setAppKeyConfigErrors({});
    setAppKeyEdit(null);
    setSecretReveal(null);
  }

  function applyAppKeysPayload(payload: AppKeysPayload) {
    setAppKeys(asArray<AppKeyRecord>(payload.keys));
    setAppKeysConfig(payload);
    setAppKeyConfigForm(appKeyConfigFormFromPayload(payload));
    setAppKeyConfigErrors({});
  }

  async function loadAppKeys() {
    try {
      const payload = await requestJSON<AppKeysPayload>("/admin/keys", undefined, adminPassword);
      applyAppKeysPayload(payload);
      if (adminPassword) setAdminPasswordVerified(true);
    } catch (err) {
      if (adminPassword && isAdminPasswordAuthError(err)) {
        setAdminPassword("");
        setAdminPasswordInput("");
        setAdminPasswordVerified(false);
        setAdminPasswordFeedback(null);
        resetAppKeyState();
      }
      throw err;
    }
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
    setSelectedLogLoading(true);
    setSelectedLogError("");
    try {
      const payload = await requestJSON<JsonRecord>(`/admin/logs/${encodeURIComponent(id)}?body_max_chars=0`, undefined, adminPassword);
      setSelectedLog(payload);
    } catch (err) {
      setSelectedLog(null);
      setSelectedLogError(errorMessage(err));
    } finally {
      setSelectedLogLoading(false);
    }
  }

  async function inspectTraceRequest(id: string) {
    if (!id) return;
    switchTab("logs");
    await selectLog(id);
  }

  function selectAdjacentLog(direction: -1 | 1) {
    const next = logs[selectedLogIndex + direction];
    if (next?.id) void selectLog(next.id);
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
        api_type: providerForm.api_type.trim(),
        models: parseProviderModels(providerForm.models_text),
        limits: JSON.parse(providerForm.limits_text || "{}")
      };
      if (endpoints.length > 0) {
        payload.endpoints = endpoints;
      }
      if (providerForm.api_key.trim()) {
        payload.api_key = providerForm.api_key;
      }
      await requestJSON<JsonRecord>("/admin/providers", { method: "POST", body: JSON.stringify(payload) }, adminPassword);
      setNotice("Provider created");
      setProviderForm({ models_text: "", name: "", api_base: "", api_type: "openai", api_key: "", endpoints_text: "", limits_text: "{}" });
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
        model: nodeForm.model.trim(),
        upstream_model: nodeForm.upstream_model.trim()
      };
      if (nodeForm.api_key.trim()) payload.api_key = nodeForm.api_key;
      if (nodeForm.api_base.trim()) {
        payload.api_base = nodeForm.api_base.trim();
      }
      if (endpoints.length > 0) {
        payload.endpoints = endpoints;
      }
      await requestJSON<JsonRecord>("/admin/nodes", { method: "POST", body: JSON.stringify(payload) }, adminPassword);
      setNotice("Node created");
      setNodeForm({ upstream_model: "", api_key: "", name: "", provider: "", model: "", api_base: "", endpoints_text: "", limits_text: "{}", modules_text: "{}", allowed_keys_text: "[]" });
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
        models_text: formatProviderModels(provider.models),
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
        upstream_model: typeof node.upstream_model === "string" ? node.upstream_model : "",
        api_key: "",
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
    if (routingEdit.form.models_text !== formatProviderModels(routingEdit.original.models)) {
      payload.models = parseProviderModels(routingEdit.form.models_text);
    }
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
    const payload: JsonRecord = changedStringFields(routingEdit.original, routingEdit.form, ["provider", "model", "api_base", "upstream_model"]);
    if (routingEdit.form.api_key.trim()) payload.api_key = routingEdit.form.api_key;
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

  async function confirmAdminPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!adminPasswordInput) {
      clearAdminPassword();
      return;
    }
    setAdminPasswordChecking(true);
    setError("");
    setNotice("");
    setAdminPasswordFeedback(null);
    try {
      const payload = await requestJSON<AppKeysPayload>("/admin/keys", undefined, adminPasswordInput);
      setAdminPassword(adminPasswordInput);
      setAdminPasswordVerified(true);
      applyAppKeysPayload(payload);
      setAdminPasswordPopoverOpen(false);
    } catch (err) {
      setAdminPassword("");
      setAdminPasswordVerified(false);
      resetAppKeyState();
      setAdminPasswordFeedback({ text: adminPasswordVerificationMessage(err), tone: "bad" });
      setAdminPasswordPopoverOpen(true);
    } finally {
      setAdminPasswordChecking(false);
    }
  }

  function clearAdminPassword() {
    setAdminPassword("");
    setAdminPasswordInput("");
    setAdminPasswordVerified(false);
    resetAppKeyState();
    setError("");
    setNotice("");
    setAdminPasswordFeedback(null);
    setAdminPasswordPopoverOpen(false);
  }

  return {
    activeTab,
    setActiveTab,
    adminPassword,
    setAdminPassword,
    adminPasswordInput,
    setAdminPasswordInput,
    adminPasswordVerified,
    setAdminPasswordVerified,
    adminPasswordChecking,
    setAdminPasswordChecking,
    adminPasswordPopoverOpen,
    setAdminPasswordPopoverOpen,
    adminPasswordFeedback,
    setAdminPasswordFeedback,
    adminPasswordControlRef,
    loading,
    setLoading,
    notice,
    setNotice,
    error,
    setError,
    health,
    setHealth,
    usage,
    setUsage,
    cacheStats,
    setCacheStats,
    cacheError,
    setCacheError,
    cacheStatus,
    setCacheStatus,
    observabilityLoading,
    setObservabilityLoading,
    observabilityTraceID,
    setObservabilityTraceID,
    catalog,
    setCatalog,
    catalogError,
    setCatalogError,
    catalogStatus,
    setCatalogStatus,
    catalogLoading,
    setCatalogLoading,
    models,
    setModels,
    modelStats,
    setModelStats,
    stopReasons,
    setStopReasons,
    toolRate,
    setToolRate,
    logs,
    setLogs,
    logMeta,
    setLogMeta,
    logsLoading,
    setLogsLoading,
    logsError,
    setLogsError,
    selectedLog,
    setSelectedLog,
    selectedLogLoading,
    setSelectedLogLoading,
    selectedLogError,
    setSelectedLogError,
    filters,
    setFilters,
    traceInput,
    setTraceInput,
    traceDetail,
    setTraceDetail,
    traceLoading,
    setTraceLoading,
    traceError,
    setTraceError,
    traceStatus,
    setTraceStatus,
    configText,
    setConfigText,
    configIssues,
    setConfigIssues,
    validationSummary,
    setValidationSummary,
    modelForm,
    setModelForm,
    modelErrors,
    setModelErrors,
    routingPools,
    setRoutingPools,
    prefixAffinity,
    setPrefixAffinity,
    providers,
    setProviders,
    nodes,
    setNodes,
    clientModels,
    setClientModels,
    routerFallbacks,
    setRouterFallbacks,
    newFallback,
    setNewFallback,
    fallbackError,
    setFallbackError,
    modelTree,
    setModelTree,
    copyModelForm,
    setCopyModelForm,
    dependencyModel,
    setDependencyModel,
    modelAncestry,
    setModelAncestry,
    modelDependents,
    setModelDependents,
    dependencyError,
    setDependencyError,
    providerForm,
    setProviderForm,
    nodeForm,
    setNodeForm,
    clientModelForm,
    setClientModelForm,
    providerErrors,
    setProviderErrors,
    nodeErrors,
    setNodeErrors,
    clientModelErrors,
    setClientModelErrors,
    routingConflict,
    setRoutingConflict,
    routingEdit,
    setRoutingEdit,
    appKeys,
    setAppKeys,
    appKeysConfig,
    setAppKeysConfig,
    appKeyForm,
    setAppKeyForm,
    appKeyErrors,
    setAppKeyErrors,
    appKeyEdit,
    setAppKeyEdit,
    appKeyConfigForm,
    setAppKeyConfigForm,
    appKeyConfigErrors,
    setAppKeyConfigErrors,
    secretReveal,
    setSecretReveal,
    healthyCount,
    quotaRows,
    cacheRows,
    catalogRows,
    catalogWarnings,
    traceRequests,
    traceAttempts,
    traceErrors,
    traceTimeline,
    logsHaveTraceIDs,
    logStopReasonOptions,
    selectedLogID,
    selectedLogIndex,
    providerNames,
    nodeNames,
    legacyModelNames,
    fallbackModelNames,
    appKeyGroupIDs,
    adminPasswordPending,
    adminPasswordStatus,
    adminPasswordStatusTone,
    adminPasswordButtonText,
    adminPasswordSubmitDisabled,
    switchTab,
    refreshAll,
    loadOverview,
    loadObservability,
    loadCatalog,
    loadLogs,
    submitLogFilters,
    clearLogFilters,
    loadTraceDashboard,
    submitTraceSearch,
    openTraceDashboard,
    loadConfig,
    loadRouting,
    resetAppKeyState,
    applyAppKeysPayload,
    loadAppKeys,
    loadModelDependencies,
    refreshRoutingState,
    startModelCopy,
    cancelModelCopy,
    copyLegacyModel,
    deleteLegacyModel,
    submitLegacyModelDelete,
    afterLegacyModelDelete,
    validateConfigEditor,
    saveConfig,
    reloadConfig,
    selectLog,
    inspectTraceRequest,
    selectAdjacentLog,
    registerModel,
    createProvider,
    createNode,
    createClientModel,
    updateFallbackSource,
    toggleFallbackTarget,
    removeFallbackRow,
    addFallbackRow,
    saveRouterFallbacks,
    deleteProvider,
    deleteNode,
    deleteClientModel,
    startProviderEdit,
    startNodeEdit,
    startClientModelEdit,
    cancelRoutingEdit,
    updateProvider,
    updateNode,
    updateClientModel,
    updateRoutingResource,
    deleteRoutingResource,
    createAppKey,
    startAppKeyEdit,
    cancelAppKeyEdit,
    updateAppKey,
    regenerateAppKey,
    deleteAppKey,
    saveAppKeysConfig,
    confirmAdminPassword,
    clearAdminPassword
  };
}

export type AdminController = ReturnType<typeof useAdminController>;
