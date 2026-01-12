/** Logs viewer JavaScript module */

const API_ENDPOINT = "/api/logs";
const DEFAULT_LIMIT = 50;

let state = {
    logs: [],
    total: 0,
    limit: DEFAULT_LIMIT,
    offset: 0,
    filters: {
        model: "",
        outcome: "",
        stop_reason: "",
        is_tool_call: "",
        start_date: "",
        end_date: "",
        search: ""
    },
    loading: false
};

const formatDate = (isoString) => {
    if (!isoString) return "--";
    const date = new Date(isoString);
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    const hours = String(date.getHours()).padStart(2, '0');
    const minutes = String(date.getMinutes()).padStart(2, '0');
    const seconds = String(date.getSeconds()).padStart(2, '0');
    return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`;
};

const formatNumber = (value) => {
    const num = Number(value || 0);
    return Number.isFinite(num) ? num.toLocaleString() : "0";
};

const getOutcomeBadge = (outcome) => {
    if (!outcome) return '<span class="badge badge-neutral">Unknown</span>';
    const map = {
        success: "success",
        error: "error",
        cancelled: "warning"
    };
    return `<span class="badge badge-${map[outcome] || "neutral"}">${outcome}</span>`;
};

const getStopReasonBadge = (stopReason) => {
    if (!stopReason) return '<span class="badge badge-neutral">--</span>';
    const map = {
        stop: "success",
        tool_calls: "tool-call",
        length: "warning",
        content_filter: "error",
        function_call: "tool-call"
    };
    return `<span class="badge badge-${map[stopReason] || "neutral"}">${stopReason}</span>`;
};

const getToolCallIndicator = (isToolCall) => {
    if (!isToolCall) return '<span style="color: var(--ink-muted)">--</span>';
    return `<span class="tool-call-icon">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M12 2L2 7l10 5 10-5-10-5z"/>
            <path d="M2 17l10 5 10-5"/>
            <path d="M2 12l10 5 10-5"/>
        </svg>
    </span>`;
};

const getDuration = (durationMs) => {
    if (!durationMs) return "--";
    if (durationMs < 1000) return `${durationMs}ms`;
    return `${(durationMs / 1000).toFixed(2)}s`;
};

const getTokens = (usageStats) => {
    if (!usageStats) return { total: '--', input: '--', output: '--' };

    const prompt = usageStats.prompt_tokens || 0;
    const completion = usageStats.completion_tokens || 0;
    const total = usageStats.total_tokens || (prompt + completion);

    // Extract details from nested objects
    const cached = usageStats.prompt_tokens_details?.cached_tokens || 0;
    const reasoning = usageStats.completion_tokens_details?.reasoning_tokens || 0;

    return {
        total: formatNumber(total),
        input: formatNumber(prompt),
        output: formatNumber(completion),
        cached: cached > 0 ? formatNumber(cached) : null,
        reasoning: reasoning > 0 ? formatNumber(reasoning) : null
    };
};

const getTokenBadges = (tokens) => {
    if (!tokens || tokens.total === '--') {
        return '<span class="badge badge-neutral">--</span>';
    }

    let tooltip = '';
    if (tokens.cached) tooltip += `Cached: ${tokens.cached}\n`;
    if (tokens.reasoning) tooltip += `Reasoning: ${tokens.reasoning}\n`;

    const badgeHtml = `
        <div class="token-badges" title="${tooltip.trim()}">
            <span class="badge badge-neutral" title="Input tokens">${tokens.input}</span>
            <span class="badge badge-primary" title="Output tokens">${tokens.output}</span>
        </div>
    `;

    return badgeHtml;
};

const buildQueryParams = () => {
    const params = new URLSearchParams();
    const { filters, limit, offset } = state;

    if (filters.model) params.set("model", filters.model);
    if (filters.outcome) params.set("outcome", filters.outcome);
    if (filters.stop_reason) params.set("stop_reason", filters.stop_reason);
    if (filters.is_tool_call) params.set("is_tool_call", filters.is_tool_call);
    if (filters.search) params.set("search", filters.search);
    if (filters.start_date) params.set("start_date", filters.start_date);
    if (filters.end_date) params.set("end_date", filters.end_date);
    params.set("limit", limit.toString());
    params.set("offset", offset.toString());

    return params.toString();
};

const fetchLogs = async () => {
    state.loading = true;
    showLoading(true);

    try {
        const query = buildQueryParams();
        const response = await fetch(`${API_ENDPOINT}?${query}`, { cache: "no-store" });

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const data = await response.json();
        state.logs = data.logs || [];
        state.total = data.total || 0;
        renderLogs();
    } catch (error) {
        console.error("Failed to fetch logs:", error);
        showError();
    } finally {
        state.loading = false;
        showLoading(false);
    }
};

const showLoading = (show) => {
    document.getElementById("loading").classList.toggle("hidden", !show);
    document.getElementById("logsTable").classList.toggle("hidden", show);
    document.getElementById("emptyState").classList.add("hidden");
    document.getElementById("pagination").classList.add("hidden");
};

const showError = () => {
    document.getElementById("loading").classList.add("hidden");
    document.getElementById("logsTable").classList.add("hidden");
    document.getElementById("emptyState").classList.remove("hidden");
    document.querySelector("#emptyState h3").textContent = "Error loading logs";
    document.querySelector("#emptyState p").textContent = "Please try again later.";
};

const showEmpty = () => {
    document.getElementById("loading").classList.add("hidden");
    document.getElementById("logsTable").classList.add("hidden");
    document.getElementById("emptyState").classList.remove("hidden");
    document.querySelector("#emptyState h3").textContent = "No logs found";
    document.querySelector("#emptyState p").textContent = "Try adjusting your filters.";
};

const renderLogs = () => {
    const tbody = document.getElementById("logsBody");

    if (state.logs.length === 0) {
        showEmpty();
        return;
    }

    document.getElementById("loading").classList.add("hidden");
    document.getElementById("logsTable").classList.remove("hidden");
    document.getElementById("pagination").classList.remove("hidden");

    tbody.innerHTML = state.logs.map(log => `
        <tr>
            <td class="clickable" data-id="${log.id}">${formatDate(log.request_time)}</td>
            <td>${escapeHtml(log.model_name || "Unknown")}</td>
            <td>${getOutcomeBadge(log.outcome)}</td>
            <td>${getStopReasonBadge(log.stop_reason)}</td>
            <td>${getToolCallIndicator(log.is_tool_call)}</td>
            <td>${getDuration(log.duration_ms)}</td>
            <td>${getTokenBadges(getTokens(log.usage_stats))}</td>
        </tr>
    `).join("");

    // Add click handlers for detail view
    tbody.querySelectorAll(".clickable").forEach(el => {
        el.addEventListener("click", () => showLogDetail(el.dataset.id));
    });

    renderPagination();
};

const renderPagination = () => {
    const { total, limit, offset } = state;
    const start = offset + 1;
    const end = Math.min(offset + limit, total);
    const hasMore = offset + limit < total;
    const hasPrev = offset > 0;

    document.getElementById("paginationInfo").textContent =
        total > 0 ? `Showing ${start}-${end} of ${formatNumber(total)} logs` : "No logs";

    document.getElementById("prevPage").disabled = !hasPrev;
    document.getElementById("nextPage").disabled = !hasMore;
};

const showLogDetail = async (logId, bodyMaxChars = null) => {
    const modal = document.getElementById("detailModal");
    const body = document.getElementById("detailBody");
    const title = document.getElementById("detailTitle");

    modal.classList.remove("hidden");
    body.innerHTML = '<div class="loading"><div class="spinner"></div><span>Loading...</span></div>';

    try {
        const detailUrl = bodyMaxChars === null
            ? `${API_ENDPOINT}/${logId}`
            : `${API_ENDPOINT}/${logId}?body_max_chars=${bodyMaxChars}`;
        const response = await fetch(detailUrl, { cache: "no-store" });

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const log = await response.json();
        title.textContent = `Request Log - ${formatDate(log.request_time)}`;

        // Stats grid with enhanced token breakdown
        const tokens = getTokens(log.usage_stats);
        const tokenStatsHtml = `
            <div class="token-stats-grid">
                <div class="token-stat-item">
                    <div class="label">Total Tokens</div>
                    <div class="value">${escapeHtml(tokens.total)}</div>
                </div>
                <div class="token-stat-item">
                    <div class="label">Input Tokens</div>
                    <div class="value">${escapeHtml(tokens.input)}</div>
                </div>
                <div class="token-stat-item">
                    <div class="label">Output Tokens</div>
                    <div class="value">${escapeHtml(tokens.output)}</div>
                </div>
                ${tokens.cached ? `
                <div class="token-stat-item">
                    <div class="label">Cached Tokens</div>
                    <div class="value highlight-cached">${escapeHtml(tokens.cached)}</div>
                </div>` : ''}
                ${tokens.reasoning ? `
                <div class="token-stat-item">
                    <div class="label">Reasoning Tokens</div>
                    <div class="value highlight-reasoning">${escapeHtml(tokens.reasoning)}</div>
                </div>` : ''}
            </div>
        `;

        const statsHtml = `
            <div class="stats-grid">
                <div class="stat-item">
                    <div class="label">Model</div>
                    <div class="value">${escapeHtml(log.model_name || "Unknown")}</div>
                </div>
                <div class="stat-item">
                    <div class="label">Outcome</div>
                    <div class="value">${escapeHtml(log.outcome || "Unknown")}</div>
                </div>
                <div class="stat-item">
                    <div class="label">Stop Reason</div>
                    <div class="value">${escapeHtml(log.stop_reason || "--")}</div>
                </div>
                <div class="stat-item">
                    <div class="label">Duration</div>
                    <div class="value">${escapeHtml(getDuration(log.duration_ms))}</div>
                </div>
                <div class="stat-item">
                    <div class="label">Tool Calls</div>
                    <div class="value">${log.is_tool_call ? "Yes" : "No"}</div>
                </div>
            </div>
        `;

        // Request body section
        const bodyPreviewChars = log.body_preview_chars ?? (typeof log.body === "string" ? log.body.length : 0);
        const bodyTotalChars = log.body_total_chars ?? bodyPreviewChars;
        const bodyNotice = log.body_truncated
            ? `<div style="margin-bottom: 10px; color: var(--ink-muted); font-size: 12px; display: flex; gap: 12px; align-items: center; flex-wrap: wrap;">
                <span>Request body truncated to ${formatNumber(bodyPreviewChars)} of ${formatNumber(bodyTotalChars)} chars.</span>
                <button class="btn btn-secondary" type="button" data-action="load-full-body">Load full body</button>
            </div>`
            : "";
        const requestSection = `
            <div class="detail-section">
                <h3>Request</h3>
                ${bodyNotice}
                <div class="content">${formatJson(log.body)}</div>
            </div>
        `;

        // Full response section
        const responseSection = log.full_response ? `
            <div class="detail-section">
                <h3>Full Response</h3>
                <div class="content">${escapeHtml(log.full_response)}</div>
            </div>
        ` : "";

        // Stream chunks section
        let chunksSection = "";
        if (log.stream_chunks && log.stream_chunks.length > 0) {
            chunksSection = `
                <div class="detail-section">
                    <h3>Stream Chunks (${log.stream_chunks.length})</h3>
                    <div class="content">${formatJson(log.stream_chunks)}</div>
                </div>
            `;
        }

        // Usage stats section
        let usageSection = "";
        if (log.usage_stats) {
            usageSection = `
                <div class="detail-section">
                    <h3>Usage Statistics</h3>
                    <div class="content">${formatJson(log.usage_stats)}</div>
                </div>
            `;
        }

        // Errors section
        let errorsSection = "";
        if (log.errors && log.errors.length > 0) {
            errorsSection = `
                <div class="detail-section">
                    <h3>Errors</h3>
                    <div class="content">${formatJson(log.errors)}</div>
                </div>
            `;
        }

        // Linked error logs
        let linkedErrorsSection = "";
        if (log.error_logs && log.error_logs.length > 0) {
            linkedErrorsSection = `
                <div class="detail-section">
                    <h3>Linked Error Logs</h3>
                    <div class="content">${formatJson(log.error_logs)}</div>
                </div>
            `;
        }

        body.innerHTML = tokenStatsHtml + statsHtml + requestSection + responseSection + chunksSection + usageSection + errorsSection + linkedErrorsSection;

        const loadFullBtn = body.querySelector('[data-action="load-full-body"]');
        if (loadFullBtn) {
            loadFullBtn.addEventListener("click", () => showLogDetail(logId, 0));
        }
    } catch (error) {
        console.error("Failed to fetch log details:", error);
        body.innerHTML = `<p style="color: var(--error-ink)">Error loading log details: ${escapeHtml(error.message)}</p>`;
    }
};

const formatJson = (data) => {
    if (!data) return "No data";
    if (typeof data === "string") {
        return escapeHtml(data);
    }
    try {
        return escapeHtml(JSON.stringify(data, null, 2));
    } catch (e) {
        return escapeHtml(String(data));
    }
};

const escapeHtml = (text) => {
    if (text === null || text === undefined) return "";
    const div = document.createElement("div");
    div.textContent = text;
    return div.innerHTML;
};

const hideModal = () => {
    document.getElementById("detailModal").classList.add("hidden");
};

const applyFilters = () => {
    state.filters = {
        model: document.getElementById("filterModel").value,
        outcome: document.getElementById("filterOutcome").value,
        stop_reason: document.getElementById("filterStopReason").value,
        is_tool_call: document.getElementById("filterToolCall").value,
        start_date: document.getElementById("filterStartDate").value,
        end_date: document.getElementById("filterEndDate").value,
        search: document.getElementById("filterSearch").value
    };
    state.offset = 0;
    fetchLogs();
};

const clearFilters = () => {
    document.getElementById("filterModel").value = "";
    document.getElementById("filterOutcome").value = "";
    document.getElementById("filterStopReason").value = "";
    document.getElementById("filterToolCall").value = "";
    document.getElementById("filterStartDate").value = "";
    document.getElementById("filterEndDate").value = "";
    document.getElementById("filterSearch").value = "";
    state.filters = {
        model: "",
        outcome: "",
        stop_reason: "",
        is_tool_call: "",
        start_date: "",
        end_date: "",
        search: ""
    };
    state.offset = 0;
    fetchLogs();
};

const prevPage = () => {
    if (state.offset > 0) {
        state.offset = Math.max(0, state.offset - state.limit);
        fetchLogs();
        window.scrollTo({ top: 0, behavior: "smooth" });
    }
};

const nextPage = () => {
    if (state.offset + state.limit < state.total) {
        state.offset += state.limit;
        fetchLogs();
        window.scrollTo({ top: 0, behavior: "smooth" });
    }
};

const initLogsUi = () => {
    // Theme toggle
    const themeButton = document.querySelector(".theme-toggle");
    if (themeButton) {
        themeButton.addEventListener("click", themeToggle);
    }

    // Filter buttons
    document.getElementById("applyFilters").addEventListener("click", applyFilters);
    document.getElementById("clearFilters").addEventListener("click", clearFilters);

    // Pagination
    document.getElementById("prevPage").addEventListener("click", prevPage);
    document.getElementById("nextPage").addEventListener("click", nextPage);

    // Modal
    document.getElementById("closeModal").addEventListener("click", hideModal);
    document.getElementById("detailModal").addEventListener("click", (e) => {
        if (e.target === e.currentTarget) hideModal();
    });

    // Enter key on search
    document.getElementById("filterSearch").addEventListener("keypress", (e) => {
        if (e.key === "Enter") applyFilters();
    });

    // Theme icons
    updateThemeIcons(ThemeManager ? ThemeManager.getCurrent() : "light");

    // Initial load
    fetchLogs();
};

document.addEventListener("DOMContentLoaded", initLogsUi);
