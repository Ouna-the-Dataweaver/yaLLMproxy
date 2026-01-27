const API_ENDPOINT = '/api/usage';
const REFRESH_INTERVAL_MS = 5000;

let hasLoadedOnce = false;

const formatNumber = (value) => {
    const num = Number(value || 0);
    return Number.isFinite(num) ? num.toLocaleString() : '0';
};

const formatNumberShort = (value) => {
    const num = Number(value || 0);
    if (!Number.isFinite(num)) return '0';

    if (num >= 1_000_000_000) {
        return (num / 1_000_000_000).toFixed(1).replace(/\.0$/, '') + 'b';
    }
    if (num >= 1_000_000) {
        return (num / 1_000_000).toFixed(1).replace(/\.0$/, '') + 'm';
    }
    if (num >= 1_000) {
        return (num / 1_000).toFixed(1).replace(/\.0$/, '') + 'k';
    }
    return num.toLocaleString();
};

const formatTime = (value) => {
    if (!value) return '--';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
        return value;
    }
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    const hours = String(date.getHours()).padStart(2, '0');
    const minutes = String(date.getMinutes()).padStart(2, '0');
    return `${year}-${month}-${day} ${hours}:${minutes}`;
};

const setText = (id, value) => {
    const el = document.getElementById(id);
    if (el) {
        el.textContent = value;
    }
};

const setLoadingState = (message, isError = false) => {
    const loadingEl = document.getElementById('loading');
    if (!loadingEl) return;
    if (isError) {
        loadingEl.innerHTML = `<strong>${message}</strong>`;
        return;
    }
    loadingEl.innerHTML = `
        <div class="spinner"></div>
        <span>${message}</span>
    `;
};

const setConnectionStatus = (ok, detail) => {
    const statusEl = document.getElementById('connectionStatus');
    if (!statusEl) return;

    statusEl.classList.remove('status-ok', 'status-warn');
    statusEl.classList.add(ok ? 'status-ok' : 'status-warn');
    statusEl.textContent = ok ? 'Online' : 'Offline';
    statusEl.title = detail || '';
};

const renderRealtime = (realtime, generatedAt) => {
    const received = realtime?.received ?? 0;
    const served = realtime?.served ?? 0;
    const ongoing = realtime?.ongoing ?? 0;
    const completion = received > 0 ? (served / received) * 100 : 0;

    setText('statReceived', formatNumber(received));
    setText('statServed', formatNumber(served));
    setText('statOngoing', formatNumber(ongoing));
    setText('statUpdated', formatTime(generatedAt));
    setText('statStarted', formatTime(realtime?.started_at));

    const grid = document.getElementById('realtime-grid');
    if (!grid) return;

    grid.innerHTML = `
        <div class="stat-card">
            <div class="stat-label">Received</div>
            <div class="stat-value">${formatNumber(received)}</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Served</div>
            <div class="stat-value">${formatNumber(served)}</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Ongoing</div>
            <div class="stat-value">${formatNumber(ongoing)}</div>
        </div>
        <div class="stat-card">
            <div class="stat-label">Completion Rate</div>
            <div class="stat-value">${completion.toFixed(1)}%</div>
        </div>
    `;
};

const renderHistorical = (historical) => {
    const errorEl = document.getElementById('history-error');
    const contentEl = document.getElementById('history-content');
    const statusEl = document.getElementById('historyStatus');
    const messageEl = document.getElementById('historyMessage');
    const subtitleEl = document.getElementById('historySubtitle');

    if (!historical?.enabled) {
        // Show error/placeholder state
        if (errorEl) errorEl.style.display = 'block';
        if (contentEl) contentEl.style.display = 'none';
        if (statusEl) {
            statusEl.textContent = historical?.status === 'error' ? 'Error' : 'Not configured';
        }
        if (messageEl) {
            messageEl.textContent = historical?.message || 'Database logging is not configured yet.';
        }
        if (subtitleEl) {
            subtitleEl.textContent = historical?.status === 'error'
                ? 'Failed to load historical data from database.'
                : 'Long-term usage analytics from the database.';
        }
        return;
    }

    // Show content state
    if (errorEl) errorEl.style.display = 'none';
    if (contentEl) contentEl.style.display = 'block';
    if (subtitleEl) {
        subtitleEl.textContent = `Data provider: ${historical.provider || 'database'}`;
    }

    // Render total stats
    const total = historical.total_stats || {};
    setText('statTotalRequests', formatNumber(total.total_requests));
    setText('statSuccessful', formatNumber(total.successful_requests));
    setText('statFailed', formatNumber(total.failed_requests));
    setText('statSuccessRate', `${total.success_rate || 0}%`);
    setText('statAvgDuration', total.avg_duration_ms ? `${Math.round(total.avg_duration_ms)}ms` : '--');

    // Render date range
    if (total.start_time && total.end_time) {
        const start = new Date(total.start_time);
        const end = new Date(total.end_time);
        setText('historyDateRange', `${formatTime(start)} - ${formatTime(end)}`);
    }

    // Render token stats
    const tokenStats = historical.token_stats || {};
    renderTokenStats(tokenStats, true);

    // Render tokens by model
    const tokensByModel = historical.tokens_by_model || [];
    renderTokensByModel(tokensByModel);

    // Render token trends
    const tokenTrends = historical.token_trends || [];
    renderTokenTrends(tokenTrends);

    // Render requests by model table
    const requestsByModel = historical.requests_by_model || [];
    const requestsBody = document.getElementById('requests-by-model');
    if (requestsBody) {
        if (requestsByModel.length === 0) {
            requestsBody.innerHTML = '<tr><td colspan="2" style="text-align: center; color: var(--ink-muted);">No data available</td></tr>';
        } else {
            requestsBody.innerHTML = requestsByModel.map(item => `
                <tr>
                    <td>${escapeHtml(item.model_name || 'Unknown')}</td>
                    <td>${formatNumber(item.count)}</td>
                </tr>
            `).join('');
        }
    }

    // Render error rates table
    const errorRates = historical.error_rates || [];
    const errorRatesBody = document.getElementById('error-rates');
    if (errorRatesBody) {
        if (errorRates.length === 0) {
            errorRatesBody.innerHTML = '<tr><td colspan="4" style="text-align: center; color: var(--ink-muted);">No data available</td></tr>';
        } else {
            errorRatesBody.innerHTML = errorRates.map(item => `
                <tr>
                    <td>${escapeHtml(item.model_name || 'Unknown')}</td>
                    <td>${formatNumber(item.total_requests)}</td>
                    <td>${formatNumber(item.error_count)}</td>
                    <td>${item.error_rate || 0}%</td>
                </tr>
            `).join('');
        }
    }

    // Render usage trends as vertical bar chart (time on X, usage on Y)
    const trends = historical.usage_trends || [];
    const trendsEl = document.getElementById('usage-trends');
    if (trendsEl) {
        // Always render 48 slots for consistent bar width
        const SLOT_COUNT = 48;

        // Build a map of existing data by truncated hour (in UTC)
        const dataByHour = new Map();
        for (const item of trends) {
            if (item.timestamp) {
                // Parse timestamp - append Z if no timezone to treat as UTC
                let ts = item.timestamp;
                if (!ts.endsWith('Z') && !ts.includes('+') && !ts.includes('-', 10)) {
                    ts = ts.replace(' ', 'T') + 'Z';
                }
                const date = new Date(ts);
                // Truncate to hour in UTC for matching
                date.setUTCMinutes(0, 0, 0);
                dataByHour.set(date.getTime(), item.count || 0);
            }
        }

        // Generate 48 hourly slots from now going back (in UTC)
        const now = new Date();
        now.setUTCMinutes(0, 0, 0); // Truncate to current hour in UTC
        const slots = [];
        for (let i = SLOT_COUNT - 1; i >= 0; i--) {
            const slotTime = new Date(now.getTime() - i * 60 * 60 * 1000);
            const count = dataByHour.get(slotTime.getTime()) || 0;
            slots.push({ timestamp: slotTime.toISOString(), count });
        }

        const maxCount = Math.max(...slots.map(t => t.count || 0), 1);

    // Format hour label - show sparingly to avoid clutter
    const formatHourLabel = (timestamp, index, all) => {
        if (!timestamp) return '';
        const date = new Date(timestamp);
        if (Number.isNaN(date.getTime())) return '';
        const hours = date.getUTCHours();
        const month = String(date.getUTCMonth() + 1).padStart(2, '0');
        const day = String(date.getUTCDate()).padStart(2, '0');

        // Show date on first bar
        if (index === 0) {
            return `${month}/${day}`;
        }
        // Check if day changed from previous
        if (index > 0 && all[index - 1]?.timestamp) {
            const prevDate = new Date(all[index - 1].timestamp);
            if (date.getUTCDate() !== prevDate.getUTCDate()) {
                return `${month}/${day}`;
            }
        }
        // Show label every 6 hours (00:00, 06:00, 12:00, 18:00)
        if (hours % 6 === 0) {
            return `${String(hours).padStart(2, '0')}:00`;
        }
        return '';
    };

    trendsEl.innerHTML = `
        <div class="usage-chart">
            <div class="chart-y-axis">
                <span class="y-label">${formatNumber(maxCount)}</span>
                <span class="y-label">${formatNumber(Math.round(maxCount / 2))}</span>
                <span class="y-label">0</span>
            </div>
            <div class="chart-area">
                <div class="chart-grid-lines">
                    <div class="grid-line"></div>
                    <div class="grid-line"></div>
                    <div class="grid-line"></div>
                </div>
                <div class="chart-bars">
                    ${slots.map((item, idx) => {
                        const count = item.count || 0;
                        const height = maxCount > 0 ? (count / maxCount) * 100 : 0;
                        const label = formatHourLabel(item.timestamp, idx, slots);
                        return `
                            <div class="chart-bar-container" title="${formatTime(item.timestamp)}: ${formatNumber(count)} requests">
                                <div class="chart-bar" style="height: ${height}%"></div>
                                <span class="chart-bar-label">${label}</span>
                            </div>
                        `;
                    }).join('')}
                </div>
            </div>
        </div>
    `;
    }

    // Render stop reason breakdown
    const stopReasons = historical.stop_reasons || [];
    renderStopReasons(stopReasons);
};

const renderStopReasons = (stopReasons) => {
    const grid = document.getElementById('stop-reasons-grid');
    if (!grid) return;

    if (!stopReasons || stopReasons.length === 0) {
        grid.innerHTML = '<p style="color: var(--ink-muted);">No stop reason data available</p>';
        return;
    }

    const badgeClass = {
        'stop': 'badge-success',
        'tool_calls': 'badge-tool-call',
        'length': 'badge-warning',
        'content_filter': 'badge-error',
        'function_call': 'badge-tool-call'
    };

    grid.innerHTML = stopReasons.map(item => `
        <div class="stat-card">
            <div class="stat-label">
                <span class="badge ${badgeClass[item.reason] || 'badge-neutral'}">
                    ${escapeHtml(item.reason || 'unknown')}
                </span>
            </div>
            <div class="stat-value">${formatNumber(item.count)}</div>
            <div class="stat-sublabel">${item.percentage}%</div>
        </div>
    `).join('');
};

const renderTokenStats = (tokenStats, isHistorical = false) => {
    if (!tokenStats) {
        if (isHistorical) {
            setText('statTotalTokensAll', '0');
            setText('statPromptTokens', '0');
            setText('statCompletionTokens', '0');
            setText('statAvgTokens', '0');
        }
        return;
    }

    const totalTokens = tokenStats.total_tokens || 0;
    const promptTokens = tokenStats.total_prompt_tokens || 0;
    const completionTokens = tokenStats.total_completion_tokens || 0;
    const avgTokens = tokenStats.avg_tokens_per_request || 0;
    const cachedTokens = tokenStats.total_cached_tokens || 0;
    const reasoningTokens = tokenStats.total_reasoning_tokens || 0;

    if (isHistorical) {
        // Use formatNumberShort for token stats with title for exact value
        const statTotalTokensEl = document.getElementById('statTotalTokensAll');
        if (statTotalTokensEl) {
            statTotalTokensEl.textContent = formatNumberShort(totalTokens);
            statTotalTokensEl.title = formatNumber(totalTokens);
        }
        const statPromptTokensEl = document.getElementById('statPromptTokens');
        if (statPromptTokensEl) {
            statPromptTokensEl.textContent = formatNumberShort(promptTokens);
            statPromptTokensEl.title = formatNumber(promptTokens);
        }
        const statCompletionTokensEl = document.getElementById('statCompletionTokens');
        if (statCompletionTokensEl) {
            statCompletionTokensEl.textContent = formatNumberShort(completionTokens);
            statCompletionTokensEl.title = formatNumber(completionTokens);
        }
        setText('statAvgTokens', formatNumber(avgTokens));

        // Show/hide cached tokens
        const cachedCard = document.getElementById('cached-tokens-card');
        if (cachedCard) {
            cachedCard.style.display = cachedTokens > 0 ? 'block' : 'none';
            if (cachedTokens > 0) {
                const statCachedTokensEl = document.getElementById('statCachedTokens');
                if (statCachedTokensEl) {
                    statCachedTokensEl.textContent = formatNumberShort(cachedTokens);
                    statCachedTokensEl.title = formatNumber(cachedTokens);
                }
            }
        }

        // Show/hide reasoning tokens
        const reasoningCard = document.getElementById('reasoning-tokens-card');
        if (reasoningCard) {
            reasoningCard.style.display = reasoningTokens > 0 ? 'block' : 'none';
            if (reasoningTokens > 0) {
                const statReasoningTokensEl = document.getElementById('statReasoningTokens');
                if (statReasoningTokensEl) {
                    statReasoningTokensEl.textContent = formatNumberShort(reasoningTokens);
                    statReasoningTokensEl.title = formatNumber(reasoningTokens);
                }
            }
        }
    }
};

const renderTokensByModel = (tokensByModel) => {
    const tbody = document.getElementById('tokens-by-model');
    if (!tbody) return;

    if (!tokensByModel || tokensByModel.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" style="text-align: center; color: var(--ink-muted);">No token data available</td></tr>';
        return;
    }

    tbody.innerHTML = tokensByModel.map(item => `
        <tr>
            <td>${escapeHtml(item.model_name || 'Unknown')}</td>
            <td title="${formatNumber(item.total_tokens)}">${formatNumberShort(item.total_tokens)}</td>
            <td title="${formatNumber(item.prompt_tokens)}">${formatNumberShort(item.prompt_tokens)}</td>
            <td title="${formatNumber(item.completion_tokens)}">${formatNumberShort(item.completion_tokens)}</td>
        </tr>
    `).join('');
};

const renderTokenTrends = (tokenTrends) => {
    const trendsEl = document.getElementById('token-trends');
    if (!trendsEl) return;

    if (!tokenTrends || tokenTrends.length === 0) {
        trendsEl.innerHTML = '<p style="color: var(--ink-muted);">No token trend data available</p>';
        return;
    }

    // Always render 48 slots for consistent bar width
    const SLOT_COUNT = 48;

    // Build a map of existing data by truncated hour (in UTC)
    const dataByHour = new Map();
    for (const item of tokenTrends) {
        if (item.timestamp) {
            // Parse timestamp - append Z if no timezone to treat as UTC
            let ts = item.timestamp;
            if (!ts.endsWith('Z') && !ts.includes('+') && !ts.includes('-', 10)) {
                ts = ts.replace(' ', 'T') + 'Z';
            }
            const date = new Date(ts);
            // Truncate to hour in UTC for matching
            date.setUTCMinutes(0, 0, 0);
            dataByHour.set(date.getTime(), item.total_tokens || 0);
        }
    }

    // Generate 48 hourly slots from now going back (in UTC)
    const now = new Date();
    now.setUTCMinutes(0, 0, 0); // Truncate to current hour in UTC
    const slots = [];
    for (let i = SLOT_COUNT - 1; i >= 0; i--) {
        const slotTime = new Date(now.getTime() - i * 60 * 60 * 1000);
        const count = dataByHour.get(slotTime.getTime()) || 0;
        slots.push({ timestamp: slotTime.toISOString(), count });
    }

    const maxCount = Math.max(...slots.map(t => t.count || 0), 1);

    // Format hour label - show sparingly to avoid clutter
    const formatHourLabel = (timestamp, index, all) => {
        if (!timestamp) return '';
        const date = new Date(timestamp);
        if (Number.isNaN(date.getTime())) return '';
        const hours = date.getUTCHours();
        const month = String(date.getUTCMonth() + 1).padStart(2, '0');
        const day = String(date.getUTCDate()).padStart(2, '0');

        // Show date on first bar
        if (index === 0) {
            return `${month}/${day}`;
        }
        // Check if day changed from previous
        if (index > 0 && all[index - 1]?.timestamp) {
            const prevDate = new Date(all[index - 1].timestamp);
            if (date.getUTCDate() !== prevDate.getUTCDate()) {
                return `${month}/${day}`;
            }
        }
        // Show label every 6 hours (00:00, 06:00, 12:00, 18:00)
        if (hours % 6 === 0) {
            return `${String(hours).padStart(2, '0')}:00`;
        }
        return '';
    };

    trendsEl.innerHTML = `
        <div class="usage-chart">
            <div class="chart-y-axis">
                <span class="y-label" title="${formatNumber(maxCount)}">${formatNumberShort(maxCount)}</span>
                <span class="y-label" title="${formatNumber(Math.round(maxCount / 2))}">${formatNumberShort(Math.round(maxCount / 2))}</span>
                <span class="y-label">0</span>
            </div>
            <div class="chart-area">
                <div class="chart-grid-lines">
                    <div class="grid-line"></div>
                    <div class="grid-line"></div>
                    <div class="grid-line"></div>
                </div>
                <div class="chart-bars">
                    ${slots.map((item, idx) => {
                        const count = item.count || 0;
                        const height = maxCount > 0 ? (count / maxCount) * 100 : 0;
                        const label = formatHourLabel(item.timestamp, idx, slots);
                        return `
                            <div class="chart-bar-container" title="${formatTime(item.timestamp)}: ${formatNumber(count)} tokens">
                                <div class="chart-bar" style="height: ${height}%"></div>
                                <span class="chart-bar-label">${label}</span>
                            </div>
                        `;
                    }).join('')}
                </div>
            </div>
        </div>
    `;
};

// Collapsible section handlers
const initCollapsibleSections = () => {
    document.querySelectorAll('.collapsible-header').forEach(header => {
        header.addEventListener('click', () => {
            const targetId = header.dataset.target;
            const target = document.getElementById(targetId);
            const toggle = header.querySelector('.collapsible-toggle');
            if (target && toggle) {
                target.classList.toggle('expanded');
                toggle.classList.toggle('expanded');
            }
        });
    });
};

// Simple HTML escape to prevent XSS
const escapeHtml = (text) => {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
};

const showContent = () => {
    const loadingEl = document.getElementById('loading');
    const contentEl = document.getElementById('usage-content');
    if (loadingEl) loadingEl.style.display = 'none';
    if (contentEl) contentEl.style.display = 'block';
};

const loadUsage = async (showSpinner = false) => {
    if (!hasLoadedOnce && showSpinner) {
        setLoadingState('Loading usage counters...');
    }

    try {
        const response = await fetch(API_ENDPOINT, { cache: 'no-store' });
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json();
        renderRealtime(data.realtime, data.generated_at);
        renderHistorical(data.historical);
        setConnectionStatus(true, '');
        if (!hasLoadedOnce) {
            showContent();
            hasLoadedOnce = true;
        }
    } catch (error) {
        console.error('Failed to load usage data:', error);
        setConnectionStatus(false, error.message || 'Fetch failed');
        if (!hasLoadedOnce) {
            setLoadingState('Failed to load usage counters.', true);
        }
    }
};

const initUsageUi = () => {
    const themeButton = document.querySelector('.theme-toggle');
    if (themeButton) {
        themeButton.addEventListener('click', themeToggle);
    }

    const refreshButton = document.getElementById('refreshBtn');
    if (refreshButton) {
        refreshButton.addEventListener('click', () => loadUsage(true));
    }

    // Initialize collapsible sections
    initCollapsibleSections();

    updateThemeIcons(ThemeManager.getCurrent());
    loadUsage(true);
    setInterval(() => loadUsage(false), REFRESH_INTERVAL_MS);
};

document.addEventListener('DOMContentLoaded', initUsageUi);
