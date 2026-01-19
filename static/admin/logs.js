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

const showLogDetail = async (logId) => {
    const modal = document.getElementById("detailModal");
    const body = document.getElementById("detailBody");
    const title = document.getElementById("detailTitle");

    // Reset IDs for new render
    treeNodeId = 0;
    sectionId = 0;
    messageId = 0;
    toolId = 0;

    modal.classList.remove("hidden");
    body.innerHTML = '<div class="loading"><div class="spinner"></div><span>Loading...</span></div>';

    try {
        // Always fetch full body (body_max_chars=0 means no truncation)
        const detailUrl = `${API_ENDPOINT}/${logId}?body_max_chars=0`;
        const response = await fetch(detailUrl, { cache: "no-store" });

        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const log = await response.json();
        title.textContent = `Request Log - ${formatDate(log.request_time)}`;

        let html = "";

        // 1. Toolbar with search and navigation
        html += renderDetailToolbar();

        // 2. Stats grid
        html += renderStatsGrid(log);

        const bodyObj = typeof log.body === "object" ? log.body : null;

        // 3. System Prompt section (Anthropic format - top level system)
        if (bodyObj && bodyObj.system) {
            html += renderSystemSection(bodyObj);
        }

        // 4. Messages section (if body is object with messages)
        if (bodyObj && bodyObj.messages) {
            html += renderMessagesSection(bodyObj.messages);
        }

        // 5. Tools section (if body has tools or functions)
        if (bodyObj && (bodyObj.tools || bodyObj.functions)) {
            html += renderToolsSection(bodyObj.tools || bodyObj.functions);
        }

        // 6. Tool Choice section (if present)
        if (bodyObj && bodyObj.tool_choice !== undefined) {
            html += renderToolChoiceSection(bodyObj);
        }

        // 7. Full Response section
        if (log.full_response) {
            const truncatedNotice = log.full_response_truncated
                ? '<span class="truncated-notice">(truncated)</span>'
                : '';
            const responseContent = escapeHtml(log.full_response);
            html += renderCollapsibleSection(
                "Full Response",
                `${truncatedNotice}<pre style="margin: 0; white-space: pre-wrap;">${responseContent}</pre>`,
                `${formatNumber(log.full_response.length)} chars`,
                true
            );
        }

        // 8. Stream Chunks section
        if (log.stream_chunks && log.stream_chunks.length > 0) {
            const truncatedNotice = log.stream_chunks_truncated
                ? `<div class="truncated-notice" style="margin-bottom: 8px;">Showing ${log.stream_chunks.length} of ${log.stream_chunks_total} chunks</div>`
                : '';
            const chunksContent = truncatedNotice + `<div class="json-tree">${renderJsonTree(log.stream_chunks)}</div>`;
            html += renderCollapsibleSection("Stream Chunks", chunksContent, `${log.stream_chunks.length} chunks`, true);
        }

        // 9. Usage Statistics section
        if (log.usage_stats) {
            const usageContent = highlightJson(JSON.stringify(log.usage_stats, null, 2));
            html += renderCollapsibleSection("Usage Statistics (Raw)", `<pre style="margin: 0; white-space: pre-wrap;">${usageContent}</pre>`, "", true);
        }

        // 10. Modules Log section
        if (log.modules_log) {
            const modulesContent = `<div class="json-tree">${renderJsonTree(log.modules_log)}</div>`;
            const eventCount = log.modules_log.total_events || 0;
            html += renderCollapsibleSection("Modules Log", modulesContent, `${eventCount} events`, true);
        }

        // 11. Backend Attempts section
        if (log.backend_attempts && log.backend_attempts.length > 0) {
            const truncatedNotice = log.backend_attempts_truncated
                ? `<div class="truncated-notice" style="margin-bottom: 8px;">Showing first ${log.backend_attempts.length} attempts</div>`
                : '';
            const attemptsContent = truncatedNotice + `<div class="json-tree">${renderJsonTree(log.backend_attempts)}</div>`;
            html += renderCollapsibleSection("Backend Attempts", attemptsContent, `${log.backend_attempts.length} attempts`, true);
        }

        // 12. Errors section (expanded by default if present)
        if (log.errors && log.errors.length > 0) {
            const errorsContent = `<div class="json-tree">${renderJsonTree(log.errors)}</div>`;
            html += renderCollapsibleSection("Errors", errorsContent, `${log.errors.length} errors`, false);
        }

        // 13. Linked Error Logs
        if (log.error_logs && log.error_logs.length > 0) {
            const errorLogsContent = `<div class="json-tree">${renderJsonTree(log.error_logs)}</div>`;
            html += renderCollapsibleSection("Linked Error Logs", errorLogsContent, `${log.error_logs.length} logs`, false);
        }

        // 14. Other Request Parameters (remaining body fields as tree view)
        if (bodyObj) {
            html += renderOtherRequestData(bodyObj);
        } else if (log.body && typeof log.body === "string") {
            // Body was truncated to a string - show as-is
            html += renderCollapsibleSection(
                "Request Body (Raw)",
                `<pre style="margin: 0; white-space: pre-wrap;">${escapeHtml(log.body)}</pre>`,
                `${formatNumber(log.body.length)} chars`,
                true
            );
        }

        body.innerHTML = html;

        // Initialize all interactive elements
        initCollapsibles(body);
        initMessageToggles(body);
        initToolToggles(body);
        initTreeToggles(body);
        initDetailSearch();
        initNavigation();
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

// ============================================================================
// Syntax Highlighting
// ============================================================================

const highlightJson = (jsonString) => {
    if (!jsonString) return "";
    const escaped = escapeHtml(jsonString);

    // Highlight JSON keys (property names)
    let highlighted = escaped.replace(
        /^(\s*)(&quot;)([^&]+)(&quot;)(\s*:)/gm,
        '$1<span class="hl-key">$2$3$4</span>$5'
    );

    // Highlight string values (after colon, not keys)
    highlighted = highlighted.replace(
        /(:\s*)(&quot;)([^&]*)(&quot;)/g,
        '$1<span class="hl-string">$2$3$4</span>'
    );

    // Highlight numbers
    highlighted = highlighted.replace(
        /(:\s*)(-?\d+\.?\d*)([\s,\n\r\]}])/g,
        '$1<span class="hl-number">$2</span>$3'
    );

    // Highlight booleans
    highlighted = highlighted.replace(
        /(:\s*)(true|false)([\s,\n\r\]}])/g,
        '$1<span class="hl-boolean">$2</span>$3'
    );

    // Highlight null
    highlighted = highlighted.replace(
        /(:\s*)(null)([\s,\n\r\]}])/g,
        '$1<span class="hl-null">$2</span>$3'
    );

    return highlighted;
};

// ============================================================================
// JSON Tree View
// ============================================================================

let treeNodeId = 0;

const renderJsonTree = (data, depth = 0, maxDepth = 10) => {
    if (depth > maxDepth) {
        return '<span class="json-tree-ellipsis">[max depth reached]</span>';
    }

    if (data === null) {
        return '<span class="json-tree-null">null</span>';
    }

    if (typeof data === "boolean") {
        return `<span class="json-tree-boolean">${data}</span>`;
    }

    if (typeof data === "number") {
        return `<span class="json-tree-number">${data}</span>`;
    }

    if (typeof data === "string") {
        const escaped = escapeHtml(data);
        // Truncate very long strings
        if (escaped.length > 500) {
            const truncated = escaped.substring(0, 500);
            return `<span class="json-tree-string">"${truncated}..."</span><span class="truncated-notice"> (${formatNumber(escaped.length)} chars)</span>`;
        }
        return `<span class="json-tree-string">"${escaped}"</span>`;
    }

    if (Array.isArray(data)) {
        if (data.length === 0) {
            return '<span class="json-tree-bracket">[]</span>';
        }

        const nodeId = `tree-${++treeNodeId}`;
        const items = data.map((item, index) => {
            return `<div class="json-tree-node"><span class="json-tree-key">${index}</span><span class="json-tree-colon">:</span>${renderJsonTree(item, depth + 1, maxDepth)}</div>`;
        }).join("");

        return `<span class="json-tree-line"><span class="json-tree-toggle" data-target="${nodeId}">▼</span><span class="json-tree-bracket">[</span><span class="json-tree-meta">${data.length} items</span></span><div class="json-tree-children" id="${nodeId}">${items}</div><span class="json-tree-bracket">]</span>`;
    }

    if (typeof data === "object") {
        const keys = Object.keys(data);
        if (keys.length === 0) {
            return '<span class="json-tree-bracket">{}</span>';
        }

        const nodeId = `tree-${++treeNodeId}`;
        const items = keys.map(key => {
            return `<div class="json-tree-node"><span class="json-tree-key">"${escapeHtml(key)}"</span><span class="json-tree-colon">:</span>${renderJsonTree(data[key], depth + 1, maxDepth)}</div>`;
        }).join("");

        return `<span class="json-tree-line"><span class="json-tree-toggle" data-target="${nodeId}">▼</span><span class="json-tree-bracket">{</span><span class="json-tree-meta">${keys.length} keys</span></span><div class="json-tree-children" id="${nodeId}">${items}</div><span class="json-tree-bracket">}</span>`;
    }

    return escapeHtml(String(data));
};

const initTreeToggles = (container) => {
    container.querySelectorAll(".json-tree-toggle").forEach(toggle => {
        toggle.addEventListener("click", (e) => {
            e.stopPropagation();
            const targetId = toggle.dataset.target;
            const target = document.getElementById(targetId);
            if (target) {
                target.classList.toggle("collapsed");
                toggle.textContent = target.classList.contains("collapsed") ? "▶" : "▼";
            }
        });
    });
};

// ============================================================================
// Collapsible Sections
// ============================================================================

let sectionId = 0;

const renderCollapsibleSection = (title, content, meta = "", startCollapsed = true) => {
    const id = `section-${++sectionId}`;
    const toggleClass = startCollapsed ? "collapsed" : "";
    const bodyClass = startCollapsed ? "" : "expanded";

    return `<div class="collapsible-section"><div class="collapsible-header" data-target="${id}"><span class="collapsible-toggle ${toggleClass}">▼</span><span class="collapsible-title">${escapeHtml(title)}</span>${meta ? `<span class="collapsible-meta">${escapeHtml(meta)}</span>` : ""}</div><div class="collapsible-body ${bodyClass}" id="${id}">${content}</div></div>`;
};

const initCollapsibles = (container) => {
    container.querySelectorAll(".collapsible-header").forEach(header => {
        header.addEventListener("click", () => {
            const targetId = header.dataset.target;
            const target = document.getElementById(targetId);
            const toggle = header.querySelector(".collapsible-toggle");
            if (target && toggle) {
                target.classList.toggle("expanded");
                toggle.classList.toggle("collapsed");
            }
        });
    });
};

// ============================================================================
// Messages Section
// ============================================================================

let messageId = 0;

const getMessagePreview = (content) => {
    if (!content) return "(empty)";

    // Handle array content (multimodal)
    if (Array.isArray(content)) {
        const textPart = content.find(p => p.type === "text");
        if (textPart && textPart.text) {
            return textPart.text.substring(0, 100);
        }
        return `[${content.length} parts]`;
    }

    // Handle string content
    if (typeof content === "string") {
        return content.substring(0, 100);
    }

    return "(complex content)";
};

const getMessageContent = (message) => {
    const content = message.content;

    // Handle null/undefined
    if (content === null || content === undefined) {
        return "(no content)";
    }

    // Handle array content (multimodal)
    if (Array.isArray(content)) {
        return content.map((part, idx) => {
            if (part.type === "text") {
                return `<div><strong>[${idx}] text:</strong>\n${escapeHtml(part.text)}</div>`;
            } else if (part.type === "image_url") {
                return `<div><strong>[${idx}] image:</strong> ${escapeHtml(part.image_url?.url?.substring(0, 100) || "")}</div>`;
            }
            return `<div><strong>[${idx}] ${escapeHtml(part.type)}:</strong>\n${escapeHtml(JSON.stringify(part, null, 2))}</div>`;
        }).join("\n");
    }

    // Handle string content
    if (typeof content === "string") {
        return escapeHtml(content);
    }

    // Handle object content
    return highlightJson(JSON.stringify(content, null, 2));
};

const renderMessagesSection = (messages) => {
    if (!messages || !Array.isArray(messages) || messages.length === 0) {
        return "";
    }

    const messagesHtml = messages.map((msg, index) => {
        const id = `msg-${++messageId}`;
        const role = msg.role || "unknown";
        const preview = getMessagePreview(msg.content);
        const fullContent = getMessageContent(msg);

        // Include tool_calls if present
        let toolCallsHtml = "";
        if (msg.tool_calls && msg.tool_calls.length > 0) {
            toolCallsHtml = `<div style="margin-top: 8px; padding-top: 8px; border-top: 1px dashed var(--border);"><strong>Tool Calls:</strong><pre style="margin: 0; white-space: pre-wrap;">${highlightJson(JSON.stringify(msg.tool_calls, null, 2))}</pre></div>`;
        }

        // Include tool_call_id if present (for tool responses)
        let toolIdHtml = "";
        if (msg.tool_call_id) {
            toolIdHtml = `<div style="margin-bottom: 8px; font-size: 11px; color: var(--ink-muted);">tool_call_id: ${escapeHtml(msg.tool_call_id)}</div>`;
        }

        return `<div class="message-item role-${escapeHtml(role)}"><div class="message-header" data-target="${id}"><span class="message-toggle collapsed">▼</span><span class="message-role">${escapeHtml(role)}</span><span class="message-preview">${escapeHtml(preview)}${preview.length >= 100 ? "..." : ""}</span></div><div class="message-content" id="${id}">${toolIdHtml}${fullContent}${toolCallsHtml}</div></div>`;
    }).join("");

    const sectionContent = `<div class="messages-list">${messagesHtml}</div>`;
    return renderCollapsibleSection("Messages", sectionContent, `${messages.length} messages`, true);
};

const initMessageToggles = (container) => {
    container.querySelectorAll(".message-header").forEach(header => {
        header.addEventListener("click", () => {
            const targetId = header.dataset.target;
            const target = document.getElementById(targetId);
            const toggle = header.querySelector(".message-toggle");
            if (target && toggle) {
                target.classList.toggle("expanded");
                toggle.classList.toggle("collapsed");
            }
        });
    });
};

// ============================================================================
// Tools Section
// ============================================================================

let toolId = 0;

const renderToolsSection = (tools) => {
    if (!tools || !Array.isArray(tools) || tools.length === 0) {
        return "";
    }

    const toolsHtml = tools.map((tool, index) => {
        const id = `tool-${++toolId}`;

        // Handle both formats: {type: "function", function: {...}} and direct function object
        const fn = tool.function || tool;
        const name = fn.name || `tool_${index}`;
        const desc = fn.description || "";
        const descPreview = desc.substring(0, 60);

        const fullContent = highlightJson(JSON.stringify(fn, null, 2));

        return `<div class="tool-item"><div class="tool-header" data-target="${id}"><span class="tool-toggle collapsed">▼</span><span class="tool-name">${escapeHtml(name)}</span><span class="tool-desc">${escapeHtml(descPreview)}${descPreview.length >= 60 ? "..." : ""}</span></div><div class="tool-content" id="${id}">${fullContent}</div></div>`;
    }).join("");

    const sectionContent = `<div class="tools-list">${toolsHtml}</div>`;
    return renderCollapsibleSection("Tools", sectionContent, `${tools.length} tools`, true);
};

const initToolToggles = (container) => {
    container.querySelectorAll(".tool-header").forEach(header => {
        header.addEventListener("click", () => {
            const targetId = header.dataset.target;
            const target = document.getElementById(targetId);
            const toggle = header.querySelector(".tool-toggle");
            if (target && toggle) {
                target.classList.toggle("expanded");
                toggle.classList.toggle("collapsed");
            }
        });
    });
};

// ============================================================================
// Search Functionality
// ============================================================================

let searchState = {
    matches: [],
    currentIndex: -1,
    originalContent: "",
    query: ""
};

const clearSearch = () => {
    const container = document.getElementById("detailBody");
    if (!container) return;

    // Remove all highlight marks
    container.querySelectorAll(".search-highlight").forEach(mark => {
        const text = document.createTextNode(mark.textContent);
        mark.parentNode.replaceChild(text, mark);
    });

    searchState = { matches: [], currentIndex: -1, originalContent: "", query: "" };
    updateSearchCount();
};

const searchInDetail = (query) => {
    if (!query || query.length < 2) {
        clearSearch();
        return;
    }

    const container = document.getElementById("detailBody");
    if (!container) return;

    // First clear previous search
    clearSearch();

    searchState.query = query;
    const regex = new RegExp(`(${escapeRegex(query)})`, "gi");

    // Walk through text nodes and highlight matches
    const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, null, false);
    const textNodes = [];

    while (walker.nextNode()) {
        if (walker.currentNode.textContent.toLowerCase().includes(query.toLowerCase())) {
            textNodes.push(walker.currentNode);
        }
    }

    textNodes.forEach(node => {
        const text = node.textContent;
        if (regex.test(text)) {
            regex.lastIndex = 0; // Reset regex
            const span = document.createElement("span");
            span.innerHTML = text.replace(regex, '<mark class="search-highlight">$1</mark>');
            node.parentNode.replaceChild(span, node);
        }
    });

    // Collect all highlights
    searchState.matches = Array.from(container.querySelectorAll(".search-highlight"));
    searchState.currentIndex = searchState.matches.length > 0 ? 0 : -1;

    if (searchState.currentIndex >= 0) {
        searchState.matches[0].classList.add("current");
        searchState.matches[0].scrollIntoView({ behavior: "smooth", block: "center" });
    }

    updateSearchCount();
};

const escapeRegex = (string) => {
    return string.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
};

const navigateSearch = (direction) => {
    if (searchState.matches.length === 0) return;

    // Remove current highlight
    if (searchState.currentIndex >= 0) {
        searchState.matches[searchState.currentIndex].classList.remove("current");
    }

    // Calculate new index
    if (direction === "next") {
        searchState.currentIndex = (searchState.currentIndex + 1) % searchState.matches.length;
    } else {
        searchState.currentIndex = (searchState.currentIndex - 1 + searchState.matches.length) % searchState.matches.length;
    }

    // Add current highlight and scroll
    searchState.matches[searchState.currentIndex].classList.add("current");
    searchState.matches[searchState.currentIndex].scrollIntoView({ behavior: "smooth", block: "center" });

    updateSearchCount();
};

const updateSearchCount = () => {
    const countEl = document.getElementById("searchCount");
    if (!countEl) return;

    if (searchState.matches.length === 0) {
        countEl.textContent = searchState.query ? "0 matches" : "";
    } else {
        countEl.textContent = `${searchState.currentIndex + 1} / ${searchState.matches.length}`;
    }
};

let searchDebounceTimer = null;

const initDetailSearch = () => {
    const input = document.getElementById("detailSearchInput");
    const prevBtn = document.getElementById("searchPrev");
    const nextBtn = document.getElementById("searchNext");

    if (input) {
        input.addEventListener("input", (e) => {
            clearTimeout(searchDebounceTimer);
            searchDebounceTimer = setTimeout(() => {
                searchInDetail(e.target.value);
            }, 200);
        });

        input.addEventListener("keydown", (e) => {
            if (e.key === "Enter") {
                e.preventDefault();
                if (e.shiftKey) {
                    navigateSearch("prev");
                } else {
                    navigateSearch("next");
                }
            }
            if (e.key === "Escape") {
                clearSearch();
                input.value = "";
            }
        });
    }

    if (prevBtn) {
        prevBtn.addEventListener("click", () => navigateSearch("prev"));
    }

    if (nextBtn) {
        nextBtn.addEventListener("click", () => navigateSearch("next"));
    }
};

// ============================================================================
// Navigation & Expand/Collapse All
// ============================================================================

const scrollDetailTo = (position) => {
    const body = document.getElementById("detailBody");
    if (!body) return;

    if (position === "top") {
        body.scrollTo({ top: 0, behavior: "smooth" });
    } else if (position === "bottom") {
        body.scrollTo({ top: body.scrollHeight, behavior: "smooth" });
    }
};

const expandAllSections = () => {
    const body = document.getElementById("detailBody");
    if (!body) return;

    body.querySelectorAll(".collapsible-body").forEach(el => el.classList.add("expanded"));
    body.querySelectorAll(".collapsible-toggle").forEach(el => el.classList.remove("collapsed"));
    body.querySelectorAll(".message-content").forEach(el => el.classList.add("expanded"));
    body.querySelectorAll(".message-toggle").forEach(el => el.classList.remove("collapsed"));
    body.querySelectorAll(".tool-content").forEach(el => el.classList.add("expanded"));
    body.querySelectorAll(".tool-toggle").forEach(el => el.classList.remove("collapsed"));
    body.querySelectorAll(".json-tree-children").forEach(el => el.classList.remove("collapsed"));
    body.querySelectorAll(".json-tree-toggle").forEach(el => el.textContent = "▼");
};

const collapseAllSections = () => {
    const body = document.getElementById("detailBody");
    if (!body) return;

    body.querySelectorAll(".collapsible-body").forEach(el => el.classList.remove("expanded"));
    body.querySelectorAll(".collapsible-toggle").forEach(el => el.classList.add("collapsed"));
    body.querySelectorAll(".message-content").forEach(el => el.classList.remove("expanded"));
    body.querySelectorAll(".message-toggle").forEach(el => el.classList.add("collapsed"));
    body.querySelectorAll(".tool-content").forEach(el => el.classList.remove("expanded"));
    body.querySelectorAll(".tool-toggle").forEach(el => el.classList.add("collapsed"));
    body.querySelectorAll(".json-tree-children").forEach(el => el.classList.add("collapsed"));
    body.querySelectorAll(".json-tree-toggle").forEach(el => el.textContent = "▶");
};

const initNavigation = () => {
    const scrollTopBtn = document.getElementById("scrollTop");
    const scrollBottomBtn = document.getElementById("scrollBottom");
    const expandAllBtn = document.getElementById("expandAll");
    const collapseAllBtn = document.getElementById("collapseAll");

    if (scrollTopBtn) {
        scrollTopBtn.addEventListener("click", () => scrollDetailTo("top"));
    }
    if (scrollBottomBtn) {
        scrollBottomBtn.addEventListener("click", () => scrollDetailTo("bottom"));
    }
    if (expandAllBtn) {
        expandAllBtn.addEventListener("click", expandAllSections);
    }
    if (collapseAllBtn) {
        collapseAllBtn.addEventListener("click", collapseAllSections);
    }
};

// ============================================================================
// Detail Toolbar Rendering
// ============================================================================

const renderDetailToolbar = () => {
    return `<div class="detail-toolbar">
        <div class="detail-search">
            <input type="text" id="detailSearchInput" placeholder="Search in content...">
            <span class="search-count" id="searchCount"></span>
            <button class="search-nav-btn" id="searchPrev" title="Previous (Shift+Enter)">↑</button>
            <button class="search-nav-btn" id="searchNext" title="Next (Enter)">↓</button>
        </div>
        <div class="expand-controls">
            <button class="expand-btn" id="expandAll" title="Expand all sections">Expand All</button>
            <button class="expand-btn" id="collapseAll" title="Collapse all sections">Collapse All</button>
        </div>
        <div class="detail-nav">
            <button class="nav-btn" id="scrollTop" title="Jump to top">↑ Top</button>
            <button class="nav-btn" id="scrollBottom" title="Jump to bottom">↓ End</button>
        </div>
    </div>`;
};

// ============================================================================
// Stats Grid Rendering
// ============================================================================

const renderStatsGrid = (log) => {
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

    return tokenStatsHtml + statsHtml;
};

// ============================================================================
// Other Request Data (remaining fields as tree view)
// ============================================================================

const KNOWN_BODY_KEYS = [
    // OpenAI Chat Completions
    "messages", "tools", "functions", "model", "stream",
    // Anthropic Messages
    "system", "tool_choice", "max_tokens", "stop_sequences", "metadata",
    // Common sampling parameters
    "temperature", "top_p", "top_k", "presence_penalty", "frequency_penalty",
    // Other common
    "n", "logprobs", "seed", "user", "response_format"
];

const renderOtherRequestData = (body) => {
    if (!body || typeof body !== "object") return "";

    // Filter out known keys
    const otherData = {};
    let hasOther = false;

    Object.keys(body).forEach(key => {
        if (!KNOWN_BODY_KEYS.includes(key)) {
            otherData[key] = body[key];
            hasOther = true;
        }
    });

    if (!hasOther) return "";

    const treeHtml = `<div class="json-tree">${renderJsonTree(otherData)}</div>`;
    return renderCollapsibleSection("Other Request Parameters", treeHtml, `${Object.keys(otherData).length} params`, true);
};

// ============================================================================
// Anthropic-Specific Request Data Rendering
// ============================================================================

const renderSystemSection = (body) => {
    if (!body || !body.system) return "";
    const system = body.system;
    let content;
    let badge;

    if (typeof system === "string") {
        // Simple string system prompt
        content = `<pre style="margin: 0; white-space: pre-wrap;">${escapeHtml(system)}</pre>`;
        badge = `${formatNumber(system.length)} chars`;
    } else if (Array.isArray(system)) {
        // Array of content blocks (anthropic format with cache_control etc)
        content = `<div class="json-tree">${renderJsonTree(system)}</div>`;
        badge = `${system.length} blocks`;
    } else {
        // Object format
        content = `<div class="json-tree">${renderJsonTree(system)}</div>`;
        badge = "object";
    }
    return renderCollapsibleSection("System Prompt", content, badge, true);
};

const renderToolChoiceSection = (body) => {
    if (!body || body.tool_choice === undefined) return "";
    const tc = body.tool_choice;
    let content;
    let badge;

    if (typeof tc === "string") {
        // Simple string like "auto", "any", "none"
        content = `<code style="font-size: 1rem;">${escapeHtml(tc)}</code>`;
        badge = tc;
    } else if (tc && typeof tc === "object") {
        // Object format like {type: "tool", name: "..."}
        content = `<div class="json-tree">${renderJsonTree(tc)}</div>`;
        badge = tc.type || "object";
    } else {
        content = `<code>${escapeHtml(String(tc))}</code>`;
        badge = String(tc);
    }
    return renderCollapsibleSection("Tool Choice", content, badge, true);
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
