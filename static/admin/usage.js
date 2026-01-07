const API_ENDPOINT = '/api/usage';
const REFRESH_INTERVAL_MS = 5000;

let hasLoadedOnce = false;

const formatNumber = (value) => {
    const num = Number(value || 0);
    return Number.isFinite(num) ? num.toLocaleString() : '0';
};

const formatTime = (value) => {
    if (!value) return '--';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
        return value;
    }
    return date.toLocaleString();
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

    // Render usage trends
    const trends = historical.usage_trends || [];
    const trendsEl = document.getElementById('usage-trends');
    if (trendsEl) {
        if (trends.length === 0) {
            trendsEl.innerHTML = '<p style="color: var(--ink-muted);">No trend data available</p>';
        } else {
            const maxCount = Math.max(...trends.map(t => t.count || 0), 1);
            trendsEl.innerHTML = `
                <div class="usage-trends">
                    ${trends.map(item => {
                        const timestamp = item.timestamp ? new Date(item.timestamp) : null;
                        const timeStr = timestamp ? formatTime(timestamp) : '--';
                        const count = item.count || 0;
                        const width = maxCount > 0 ? (count / maxCount) * 100 : 0;
                        return `
                            <div class="trend-bar">
                                <span class="trend-time">${timeStr}</span>
                                <div class="trend-track">
                                    <div class="trend-fill" style="width: ${width}%"></div>
                                </div>
                                <span class="trend-count">${formatNumber(count)}</span>
                            </div>
                        `;
                    }).join('')}
                </div>
            `;
        }
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

    updateThemeIcons(ThemeManager.getCurrent());
    loadUsage(true);
    setInterval(() => loadUsage(false), REFRESH_INTERVAL_MS);
};

document.addEventListener('DOMContentLoaded', initUsageUi);
