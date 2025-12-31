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
    const statusEl = document.getElementById('historyStatus');
    const messageEl = document.getElementById('historyMessage');
    if (statusEl) {
        statusEl.textContent = historical?.enabled ? 'Enabled' : 'Not configured';
    }
    if (messageEl) {
        messageEl.textContent = historical?.message || 'Database logging is not configured yet.';
    }
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
