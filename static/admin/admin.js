const API_BASE = '/admin';
const ADMIN_PASSWORD_HEADER = 'x-admin-password';
const ADMIN_PASSWORD_KEY = 'yallmp_admin_password';


const Icons = {
    openai: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M12 2L2 7l10 5 10-5-10-5z"/>
        <path d="M2 17l10 5 10-5"/>
        <path d="M2 12l10 5 10-5"/>
    </svg>`,
    anthropic: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="10"/>
        <path d="M12 16v-4"/>
        <path d="M12 8h.01"/>
    </svg>`,
    reasoning: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"/>
    </svg>`,
    success: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
        <polyline points="22 4 12 14.01 9 11.01"/>
    </svg>`,
    error: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="10"/>
        <line x1="15" y1="9" x2="9" y2="15"/>
        <line x1="9" y1="9" x2="15" y2="15"/>
    </svg>`,
    edit: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
        <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
    </svg>`,
    trash: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <polyline points="3 6 5 6 21 6"/>
        <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
    </svg>`,
    copy: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
        <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
    </svg>`,
    fork: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="18" cy="5" r="3"/>
        <circle cx="6" cy="12" r="3"/>
        <circle cx="18" cy="19" r="3"/>
        <line x1="8.59" y1="13.51" x2="15.42" y2="17.49"/>
        <line x1="15.41" y1="6.51" x2="8.59" y2="10.49"/>
    </svg>`,
    lock: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
        <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
    </svg>`,
    runtime: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M12 2v4"/>
        <path d="M12 18v4"/>
        <path d="M4.93 4.93l2.83 2.83"/>
        <path d="M16.24 16.24l2.83 2.83"/>
        <path d="M2 12h4"/>
        <path d="M18 12h4"/>
        <path d="M4.93 19.07l2.83-2.83"/>
        <path d="M16.24 7.76l2.83-2.83"/>
    </svg>`
};

const PARAMETER_FIELDS = [
    { name: 'temperature', inputId: 'param_temperature', overrideId: 'param_temperature_override', parser: parseFloat },
    { name: 'top_p', inputId: 'param_top_p', overrideId: 'param_top_p_override', parser: parseFloat },
    { name: 'top_k', inputId: 'param_top_k', overrideId: 'param_top_k_override', parser: (value) => parseInt(value, 10) }
];

const RESPONSE_MODULES = ['parse_unparsed', 'parse_template', 'swap_reasoning_content'];
const RESPONSE_ALIASES = {
    parse_unparsed_tags: 'parse_unparsed',
    parse_tags: 'parse_unparsed',
    parse_unparsed_template: 'parse_template',
    swap_reasoning: 'swap_reasoning_content'
};

const MODULE_IDS = {
    parse_unparsed: 'module_parse_unparsed',
    parse_template: 'module_parse_template',
    swap_reasoning_content: 'module_swap_reasoning'
};

let adminPassword = '';
let currentParametersConfig = null;
let currentParametersSource = 'model_params';
let currentModulesConfig = null;

function readStoredAdminPassword() {
    let stored = '';
    try {
        stored = sessionStorage.getItem(ADMIN_PASSWORD_KEY) || '';
    } catch (error) {
        stored = '';
    }
    if (!stored) {
        try {
            stored = localStorage.getItem(ADMIN_PASSWORD_KEY) || '';
        } catch (error) {
            stored = '';
        }
    }
    return stored;
}

function setAdminPassword(value, remember) {
    const next = (value || '').trim();
    adminPassword = next;
    try {
        sessionStorage.removeItem(ADMIN_PASSWORD_KEY);
    } catch (error) {
        // ignore storage errors
    }
    try {
        localStorage.removeItem(ADMIN_PASSWORD_KEY);
    } catch (error) {
        // ignore storage errors
    }
    if (!next) {
        return;
    }
    if (remember) {
        try {
            localStorage.setItem(ADMIN_PASSWORD_KEY, next);
        } catch (error) {
            // ignore storage errors
        }
    } else {
        try {
            sessionStorage.setItem(ADMIN_PASSWORD_KEY, next);
        } catch (error) {
            // ignore storage errors
        }
    }
}

function clearAdminPassword() {
    adminPassword = '';
    try {
        sessionStorage.removeItem(ADMIN_PASSWORD_KEY);
    } catch (error) {
        // ignore storage errors
    }
    try {
        localStorage.removeItem(ADMIN_PASSWORD_KEY);
    } catch (error) {
        // ignore storage errors
    }
}

function isAdminUnlocked() {
    return Boolean(adminPassword);
}

function getAdminHeaders() {
    const headers = {};
    if (adminPassword) {
        headers[ADMIN_PASSWORD_HEADER] = adminPassword;
    }
    return headers;
}

function updateAdminStatus() {
    const status = document.getElementById('adminStatus');
    const statusText = document.getElementById('adminStatusText');
    const button = document.getElementById('adminAccessBtn');
    if (!status || !statusText || !button) {
        return;
    }

    if (isAdminUnlocked()) {
        status.classList.add('unlocked');
        statusText.textContent = 'Admin unlocked';
        button.textContent = 'Lock';
        button.title = 'Clear admin password';
    } else {
        status.classList.remove('unlocked');
        statusText.textContent = 'Admin locked';
        button.textContent = 'Unlock';
        button.title = 'Unlock protected models';
    }
}

function openAdminModal() {
    const modal = document.getElementById('adminModal');
    const input = document.getElementById('admin_password');
    const remember = document.getElementById('adminRemember');
    if (!modal || !input || !remember) {
        return;
    }
    input.value = '';
    remember.checked = false;
    modal.classList.add('active');
    setTimeout(() => {
        input.focus();
    }, 100);
}

function closeAdminModal() {
    const modal = document.getElementById('adminModal');
    if (modal) {
        modal.classList.remove('active');
    }
}

adminPassword = readStoredAdminPassword();

function handleAdminAccessClick() {
    if (isAdminUnlocked()) {
        if (!confirm('Clear admin access for this browser session?')) {
            return;
        }
        clearAdminPassword();
        updateAdminStatus();
        showNotification('Admin access locked', 'success');
        loadModels();
    } else {
        openAdminModal();
    }
}

function saveAdminAccess(event) {
    event.preventDefault();
    const input = document.getElementById('admin_password');
    const remember = document.getElementById('adminRemember');
    if (!input || !remember) {
        return;
    }
    const value = input.value.trim();
    if (!value) {
        showNotification('Admin password is required', 'error');
        return;
    }
    setAdminPassword(value, remember.checked);
    updateAdminStatus();
    closeAdminModal();
    showNotification('Admin access unlocked', 'success');
    loadModels();
}

async function fetchModels() {
    try {
        const response = await fetch(`${API_BASE}/models`);
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        return await response.json();
    } catch (error) {
        console.error('Error loading models:', error);
        showNotification('Failed to load models', 'error');
        return [];
    }
}

async function fetchModelTree() {
    try {
        const response = await fetch(`${API_BASE}/models/tree`);
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
        }
        return await response.json();
    } catch (error) {
        console.error('Error loading model tree:', error);
        showNotification('Failed to load model tree', 'error');
        return null;
    }
}

function renderModels(payload) {
    const protectedModels = payload?.protected || [];
    const unprotectedModels = payload?.unprotected || [];

    const protectedCountEl = document.getElementById('protectedCount');
    const unprotectedCountEl = document.getElementById('unprotectedCount');
    if (protectedCountEl) {
        protectedCountEl.textContent = protectedModels.length;
    }
    if (unprotectedCountEl) {
        unprotectedCountEl.textContent = unprotectedModels.length;
    }

    renderModelList('protectedModelList', protectedModels, {
        title: 'No protected models configured',
        description: 'Mark models as protected in config.yaml to lock them.',
        showAdd: false,
        transparentIcon: false
    });
    renderModelList('unprotectedModelList', unprotectedModels, {
        title: 'No unprotected models yet',
        description: 'Register models via the admin UI or API to populate this list.',
        showAdd: true,
        transparentIcon: true
    });
}

function renderModelList(containerId, models, emptyState) {
    const container = document.getElementById(containerId);
    if (!container) {
        return;
    }

    if (!models || models.length === 0) {
        const addButton = emptyState.showAdd ? `
            <button class="btn btn-primary" type="button" data-action="add-model">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <line x1="12" y1="5" x2="12" y2="19"/>
                    <line x1="5" y1="12" x2="19" y2="12"/>
                </svg>
                Add Your First Model
            </button>
        ` : '';
        const iconClass = emptyState.transparentIcon ? 'empty-icon empty-icon-clear' : 'empty-icon';

        container.innerHTML = `
            <div class="empty-state">
                <div class="${iconClass}">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/>
                        <line x1="8" y1="21" x2="16" y2="21"/>
                        <line x1="12" y1="17" x2="12" y2="21"/>
                    </svg>
                </div>
                <h3 class="empty-title">${emptyState.title}</h3>
                <p class="empty-description">${emptyState.description}</p>
                ${addButton}
            </div>
        `;
        return;
    }

    container.innerHTML = renderModelCards(models);
}

function renderModelTree(payload) {
    const container = document.getElementById('modelTree');
    if (!container) {
        return;
    }

    if (!payload || !payload.nodes || Object.keys(payload.nodes).length === 0) {
        container.innerHTML = `
            <div class="empty-state">
                <div class="empty-icon">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M12 2L2 7l10 5 10-5-10-5z"/>
                        <path d="M2 17l10 5 10-5"/>
                        <path d="M2 12l10 5 10-5"/>
                    </svg>
                </div>
                <h3 class="empty-title">No model relationships yet</h3>
                <p class="empty-description">Add models with extends to build the inheritance tree.</p>
            </div>
        `;
        return;
    }

    const nodes = payload.nodes || {};
    const roots = payload.roots || [];
    const rows = [];

    function walk(name, depth) {
        const node = nodes[name];
        if (!node) {
            return;
        }
        rows.push({ name, node, depth });
        const children = node.children || [];
        children.forEach(child => walk(child, depth + 1));
    }

    roots.forEach(root => walk(root, 0));

    container.innerHTML = rows.map(row => {
        const parent = row.node.parent;
        const protectedModel = row.node.protected === true;
        const protectionLabel = protectedModel ? 'Protected' : 'Unprotected';
        const derivedLabel = parent ? 'Derived' : 'Root';
        const parentLabel = parent ? `extends ${escapeHtml(parent)}` : 'base model';

        return `
            <div class="tree-row" data-depth="${row.depth}" style="--depth: ${row.depth};">
                <div class="tree-dot"></div>
                <div class="tree-content">
                    <div class="tree-name">${escapeHtml(row.name)}</div>
                    <div class="tree-meta">
                        <span class="tree-pill ${protectedModel ? 'tree-pill-locked' : ''}">${protectionLabel}</span>
                        <span class="tree-pill ${parent ? 'tree-pill-derived' : ''}">${derivedLabel}</span>
                        <span class="tree-parent">${parentLabel}</span>
                    </div>
                </div>
            </div>
        `;
    }).join('');
}

function renderModelCards(models) {
    return models.map((model, index) => {
        const params = model.model_params || {};
        const apiType = params.api_type || 'openai';
        const supportsReasoning = params.supports_reasoning;
        const protectedModel = model.protected === true;
        const adminUnlocked = isAdminUnlocked();
        const editable = !protectedModel || adminUnlocked;
        const canCopy = !protectedModel || adminUnlocked;
        const safeModelName = escapeHtml(model.model_name);
        const protectionLabel = protectedModel ? 'Protected' : 'Unprotected';
        const inheritedFrom = model._inherited_from;

        let badges = '';
        if (apiType === 'openai') {
            badges += `<span class="badge badge-openai">${Icons.openai}OpenAI</span>`;
        } else if (apiType === 'anthropic') {
            badges += `<span class="badge badge-anthropic">${Icons.anthropic}Anthropic</span>`;
        }
        if (supportsReasoning) {
            badges += `<span class="badge badge-reasoning">${Icons.reasoning}Reasoning</span>`;
        }

        // Add inherited/derived badge
        if (inheritedFrom) {
            badges += `<span class="badge badge-derived" title="Inherits from ${escapeHtml(inheritedFrom)}">${Icons.fork}Derived</span>`;
        }

        // Add editable/locked badge
        if (editable) {
            badges += `<span class="badge badge-editable">${protectionLabel}</span>`;
        } else {
            badges += `<span class="badge badge-locked">${protectionLabel}</span>`;
        }

        const details = [];
        details.push({ label: 'API Endpoint', value: params.api_base });
        if (params.model) {
            details.push({ label: 'Upstream Model', value: params.model });
        }
        details.push({ label: 'Timeout', value: `${params.request_timeout || 'Default'}s` });

        // Show inherited from info if applicable
        if (inheritedFrom) {
            details.push({ label: 'Inherits From', value: inheritedFrom });
        }

        // Build action buttons with disabled state for non-editable models
        const editDisabled = !editable ? 'btn-disabled tooltip' : '';
        const deleteDisabled = !editable ? 'btn-disabled tooltip' : '';
        const copyDisabled = !canCopy ? 'btn-disabled tooltip' : '';
        const editTooltip = !editable ? 'data-tooltip="Protected models require an admin password"' : '';
        const deleteTooltip = !editable ? 'data-tooltip="Protected models require an admin password"' : '';
        const copyTooltip = !canCopy ? 'data-tooltip="Protected models require an admin password"' : '';
        const editAria = !editable ? 'aria-disabled="true"' : 'aria-disabled="false"';
        const deleteAria = !editable ? 'aria-disabled="true"' : 'aria-disabled="false"';
        const copyAria = !canCopy ? 'aria-disabled="true"' : 'aria-disabled="false"';

        return `
            <div class="model-item" style="animation-delay: ${index * 0.08}s">
                <div class="model-info">
                    <div class="model-name-row">
                        <span class="model-name">${safeModelName}</span>
                        <div class="badges">${badges}</div>
                    </div>
                    <div class="model-details">
                        ${details.map(d => `
                            <div class="model-detail">
                                <span class="model-detail-label">${escapeHtml(d.label)}</span>
                                <span class="model-detail-value">${escapeHtml(d.value)}</span>
                            </div>
                        `).join('')}
                    </div>
                </div>
                <div class="model-actions">
                    <button class="btn btn-secondary btn-icon ${copyDisabled}"
                            type="button"
                            data-action="copy"
                            data-model="${safeModelName}"
                            ${copyTooltip}
                            ${copyAria}
                            title="Copy model">
                        ${Icons.copy}
                    </button>
                    <button class="btn btn-secondary btn-icon ${editDisabled}"
                            type="button"
                            data-action="edit"
                            data-model="${safeModelName}"
                            ${editTooltip}
                            ${editAria}
                            title="${editable ? 'Edit model' : 'Protected models require an admin password'}">
                        ${Icons.edit}
                    </button>
                    <button class="btn btn-danger btn-icon ${deleteDisabled}"
                            type="button"
                            data-action="delete"
                            data-model="${safeModelName}"
                            ${deleteTooltip}
                            ${deleteAria}
                            title="${editable ? 'Delete model' : 'Protected models require an admin password'}">
                        ${Icons.trash}
                    </button>
                </div>
            </div>
        `;
    }).join('');
}

function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function deepClone(value) {
    if (!value || typeof value !== 'object') {
        return value;
    }
    try {
        return JSON.parse(JSON.stringify(value));
    } catch (error) {
        return value;
    }
}

function parseNumber(value, parser) {
    const raw = (value || '').toString().trim();
    if (!raw) {
        return undefined;
    }
    const parsed = parser(raw);
    if (Number.isNaN(parsed)) {
        return undefined;
    }
    return parsed;
}

function parseCommaList(value) {
    if (!value) {
        return [];
    }
    return value
        .split(',')
        .map(item => item.trim())
        .filter(Boolean);
}

function setOptionalField(obj, key, value) {
    if (!obj) {
        return;
    }
    if (value === undefined || value === null || value === '') {
        delete obj[key];
    } else {
        obj[key] = value;
    }
}

function getParameterOverridesInfo(model) {
    if (!model) {
        return { params: {}, source: 'model_params' };
    }
    if (model.parameters && typeof model.parameters === 'object') {
        return { params: model.parameters, source: 'top' };
    }
    const params = model.model_params || {};
    if (params.parameters && typeof params.parameters === 'object') {
        return { params: params.parameters, source: 'model_params' };
    }
    return { params: {}, source: 'model_params' };
}

function resetParametersForm() {
    PARAMETER_FIELDS.forEach(field => {
        const input = document.getElementById(field.inputId);
        const toggle = document.getElementById(field.overrideId);
        if (input) {
            input.value = '';
        }
        if (toggle) {
            toggle.checked = true;
        }
    });
}

function fillParametersForm(parameters) {
    PARAMETER_FIELDS.forEach(field => {
        const input = document.getElementById(field.inputId);
        const toggle = document.getElementById(field.overrideId);
        const config = parameters ? parameters[field.name] : undefined;
        if (input) {
            if (config && config.default !== undefined && config.default !== null) {
                input.value = config.default;
            } else {
                input.value = '';
            }
        }
        if (toggle) {
            if (config && typeof config.allow_override === 'boolean') {
                toggle.checked = config.allow_override;
            } else {
                toggle.checked = true;
            }
        }
    });
}

function buildParameterOverrides() {
    const base = deepClone(currentParametersConfig) || {};
    PARAMETER_FIELDS.forEach(field => {
        const input = document.getElementById(field.inputId);
        const toggle = document.getElementById(field.overrideId);
        if (!input || !toggle) {
            return;
        }
        const value = parseNumber(input.value, field.parser);
        if (value === undefined) {
            delete base[field.name];
            return;
        }
        const existing = base[field.name] && typeof base[field.name] === 'object'
            ? base[field.name]
            : {};
        base[field.name] = {
            ...existing,
            default: value,
            allow_override: toggle.checked
        };
    });
    Object.keys(base).forEach(key => {
        const cfg = base[key];
        if (!cfg || typeof cfg !== 'object' || Object.keys(cfg).length === 0) {
            delete base[key];
        }
    });
    return Object.keys(base).length ? base : undefined;
}

function getModulesConfig(model) {
    if (!model) {
        return null;
    }
    if (model.modules && typeof model.modules === 'object') {
        return model.modules;
    }
    if (model.parsers && typeof model.parsers === 'object') {
        return model.parsers;
    }
    const params = model.model_params || {};
    if (params.modules && typeof params.modules === 'object') {
        return params.modules;
    }
    if (params.parsers && typeof params.parsers === 'object') {
        return params.parsers;
    }
    return null;
}

function normalizeModulesConfig(modulesCfg) {
    if (!modulesCfg || typeof modulesCfg !== 'object') {
        return null;
    }
    if (modulesCfg.upstream || modulesCfg.downstream) {
        const upstreamRaw = modulesCfg.upstream && typeof modulesCfg.upstream === 'object'
            ? modulesCfg.upstream
            : {};
        const upstream = { ...upstreamRaw };
        if (upstream.enabled === undefined && modulesCfg.enabled !== undefined) {
            upstream.enabled = modulesCfg.enabled;
        }
        return { upstream };
    }
    return { upstream: { ...modulesCfg } };
}

function normalizeResponseNames(names) {
    const normalized = new Set();
    (names || []).forEach(name => {
        if (!name) {
            return;
        }
        const key = RESPONSE_ALIASES[name] || name;
        normalized.add(key);
    });
    return Array.from(normalized);
}

function resetModulesForm() {
    const override = document.getElementById('modules_override');
    const enabled = document.getElementById('modules_enabled');
    const paths = document.getElementById('modules_paths');
    if (override) {
        override.checked = false;
    }
    if (enabled) {
        enabled.checked = true;
    }
    if (paths) {
        paths.value = '';
    }
    RESPONSE_MODULES.forEach(name => {
        const checkbox = document.getElementById(MODULE_IDS[name]);
        if (checkbox) {
            checkbox.checked = false;
        }
    });
    document.getElementById('parse_unparsed_think_tag').value = 'think';
    document.getElementById('parse_unparsed_tool_tag').value = 'tool_call';
    document.getElementById('parse_unparsed_tool_buffer_limit').value = '';
    document.getElementById('parse_unparsed_parse_thinking').checked = true;
    document.getElementById('parse_unparsed_parse_tool_calls').checked = true;

    document.getElementById('parse_template_path').value = '';
    document.getElementById('parse_template_think_tag').value = '';
    document.getElementById('parse_template_tool_tag').value = '';
    document.getElementById('parse_template_tool_format').value = 'auto';
    document.getElementById('parse_template_tool_buffer_limit').value = '';
    document.getElementById('parse_template_parse_thinking').checked = true;
    document.getElementById('parse_template_parse_tool_calls').checked = true;

    document.getElementById('swap_reasoning_mode').value = 'reasoning_to_content';
    document.getElementById('swap_reasoning_think_tag').value = 'think';
    document.getElementById('swap_reasoning_open_prefix').value = '';
    document.getElementById('swap_reasoning_open_suffix').value = '';
    document.getElementById('swap_reasoning_close_prefix').value = '';
    document.getElementById('swap_reasoning_close_suffix').value = '';
    document.getElementById('swap_reasoning_include_newline').checked = true;

    updateModulesVisibility();
}

function fillModulesForm(upstreamCfg) {
    const override = document.getElementById('modules_override');
    const enabled = document.getElementById('modules_enabled');
    const paths = document.getElementById('modules_paths');
    if (override) {
        override.checked = Boolean(upstreamCfg);
    }
    if (enabled) {
        enabled.checked = upstreamCfg?.enabled !== undefined ? Boolean(upstreamCfg.enabled) : true;
    }
    if (paths) {
        paths.value = (upstreamCfg?.paths || []).join(', ');
    }

    const responseList = normalizeResponseNames(upstreamCfg?.response || []);
    RESPONSE_MODULES.forEach(name => {
        const checkbox = document.getElementById(MODULE_IDS[name]);
        if (checkbox) {
            checkbox.checked = responseList.includes(name);
        }
    });

    const parseUnparsedCfg =
        upstreamCfg?.parse_unparsed ||
        upstreamCfg?.parse_unparsed_tags ||
        upstreamCfg?.parse_tags ||
        {};
    populateTemplateSelect('parse_unparsed_template_path', parseUnparsedCfg.template_path || '', '-- None (use manual tags) --');
    document.getElementById('parse_unparsed_think_tag').value = parseUnparsedCfg.think_tag || 'think';
    document.getElementById('parse_unparsed_tool_tag').value = parseUnparsedCfg.tool_tag || 'tool_call';
    document.getElementById('parse_unparsed_tool_buffer_limit').value =
        parseUnparsedCfg.tool_buffer_limit ?? '';
    document.getElementById('parse_unparsed_parse_thinking').checked =
        parseUnparsedCfg.parse_thinking !== undefined ? Boolean(parseUnparsedCfg.parse_thinking) : true;
    document.getElementById('parse_unparsed_parse_tool_calls').checked =
        parseUnparsedCfg.parse_tool_calls !== undefined ? Boolean(parseUnparsedCfg.parse_tool_calls) : true;

    const parseTemplateCfg =
        upstreamCfg?.parse_template ||
        upstreamCfg?.parse_unparsed_template ||
        {};
    populateTemplateSelect('parse_template_path', parseTemplateCfg.template_path || '', '-- Select a template --');
    document.getElementById('parse_template_think_tag').value = parseTemplateCfg.think_tag || '';
    document.getElementById('parse_template_tool_tag').value = parseTemplateCfg.tool_tag || '';
    document.getElementById('parse_template_tool_format').value = parseTemplateCfg.tool_format || 'auto';
    document.getElementById('parse_template_tool_buffer_limit').value =
        parseTemplateCfg.tool_buffer_limit ?? '';
    document.getElementById('parse_template_parse_thinking').checked =
        parseTemplateCfg.parse_thinking !== undefined ? Boolean(parseTemplateCfg.parse_thinking) : true;
    document.getElementById('parse_template_parse_tool_calls').checked =
        parseTemplateCfg.parse_tool_calls !== undefined ? Boolean(parseTemplateCfg.parse_tool_calls) : true;

    const swapCfg = upstreamCfg?.swap_reasoning_content || upstreamCfg?.swap_reasoning || {};
    document.getElementById('swap_reasoning_mode').value = swapCfg.mode || 'reasoning_to_content';
    document.getElementById('swap_reasoning_think_tag').value = swapCfg.think_tag || 'think';
    document.getElementById('swap_reasoning_open_prefix').value = swapCfg.think_open?.prefix || '';
    document.getElementById('swap_reasoning_open_suffix').value = swapCfg.think_open?.suffix || '';
    document.getElementById('swap_reasoning_close_prefix').value = swapCfg.think_close?.prefix || '';
    document.getElementById('swap_reasoning_close_suffix').value = swapCfg.think_close?.suffix || '';
    document.getElementById('swap_reasoning_include_newline').checked =
        swapCfg.include_newline !== undefined ? Boolean(swapCfg.include_newline) : true;

    updateModulesVisibility();
}

function updateModulesVisibility() {
    const override = document.getElementById('modules_override');
    const configSection = document.getElementById('modulesConfig');
    const overrideEnabled = override && override.checked;
    if (configSection) {
        configSection.classList.toggle('hidden', !overrideEnabled);
    }
    RESPONSE_MODULES.forEach(name => {
        const checkbox = document.getElementById(MODULE_IDS[name]);
        const settings = document.querySelector(`.module-settings[data-module="${name}"]`);
        if (!checkbox || !settings) {
            return;
        }
        settings.classList.toggle('hidden', !overrideEnabled || !checkbox.checked);
    });
}

function buildModulesConfig() {
    const override = document.getElementById('modules_override');
    if (!override || !override.checked) {
        return undefined;
    }

    const base = currentModulesConfig ? deepClone(currentModulesConfig.upstream || {}) : {};
    const enabled = document.getElementById('modules_enabled').checked;
    base.enabled = enabled;

    const response = RESPONSE_MODULES.filter(name => {
        const checkbox = document.getElementById(MODULE_IDS[name]);
        return checkbox && checkbox.checked;
    });
    if (enabled && response.length === 0) {
        showNotification('Select at least one response module or disable modules', 'error');
        return null;
    }
    if (response.length) {
        base.response = response;
    } else {
        delete base.response;
    }

    const paths = parseCommaList(document.getElementById('modules_paths').value);
    if (paths.length) {
        base.paths = paths;
    } else {
        delete base.paths;
    }

    if (response.includes('parse_unparsed')) {
        const cfg = { ...(base.parse_unparsed || base.parse_unparsed_tags || base.parse_tags || {}) };
        setOptionalField(cfg, 'template_path', document.getElementById('parse_unparsed_template_path').value.trim() || undefined);
        setOptionalField(cfg, 'think_tag', document.getElementById('parse_unparsed_think_tag').value.trim() || undefined);
        setOptionalField(cfg, 'tool_tag', document.getElementById('parse_unparsed_tool_tag').value.trim() || undefined);
        const bufferLimit = parseNumber(
            document.getElementById('parse_unparsed_tool_buffer_limit').value,
            (value) => parseInt(value, 10)
        );
        setOptionalField(cfg, 'tool_buffer_limit', bufferLimit);
        cfg.parse_thinking = document.getElementById('parse_unparsed_parse_thinking').checked;
        cfg.parse_tool_calls = document.getElementById('parse_unparsed_parse_tool_calls').checked;
        base.parse_unparsed = cfg;
        delete base.parse_unparsed_tags;
        delete base.parse_tags;
    } else {
        delete base.parse_unparsed;
        delete base.parse_unparsed_tags;
        delete base.parse_tags;
    }

    if (response.includes('parse_template')) {
        const cfg = { ...(base.parse_template || base.parse_unparsed_template || {}) };
        const templatePath = document.getElementById('parse_template_path').value.trim();
        if (!templatePath) {
            showNotification('Template path is required when Parse Template is enabled', 'error');
            return null;
        }
        cfg.template_path = templatePath;
        setOptionalField(cfg, 'think_tag', document.getElementById('parse_template_think_tag').value.trim() || undefined);
        setOptionalField(cfg, 'tool_tag', document.getElementById('parse_template_tool_tag').value.trim() || undefined);
        setOptionalField(cfg, 'tool_format', document.getElementById('parse_template_tool_format').value || undefined);
        const bufferLimit = parseNumber(
            document.getElementById('parse_template_tool_buffer_limit').value,
            (value) => parseInt(value, 10)
        );
        setOptionalField(cfg, 'tool_buffer_limit', bufferLimit);
        cfg.parse_thinking = document.getElementById('parse_template_parse_thinking').checked;
        cfg.parse_tool_calls = document.getElementById('parse_template_parse_tool_calls').checked;
        base.parse_template = cfg;
        delete base.parse_unparsed_template;
    } else {
        delete base.parse_template;
        delete base.parse_unparsed_template;
    }

    if (response.includes('swap_reasoning_content')) {
        const cfg = { ...(base.swap_reasoning_content || base.swap_reasoning || {}) };
        setOptionalField(cfg, 'mode', document.getElementById('swap_reasoning_mode').value || undefined);
        setOptionalField(cfg, 'think_tag', document.getElementById('swap_reasoning_think_tag').value.trim() || undefined);
        cfg.include_newline = document.getElementById('swap_reasoning_include_newline').checked;
        const thinkOpen = { ...(cfg.think_open || {}) };
        setOptionalField(thinkOpen, 'prefix', document.getElementById('swap_reasoning_open_prefix').value);
        setOptionalField(thinkOpen, 'suffix', document.getElementById('swap_reasoning_open_suffix').value);
        if (Object.keys(thinkOpen).length) {
            cfg.think_open = thinkOpen;
        } else {
            delete cfg.think_open;
        }
        const thinkClose = { ...(cfg.think_close || {}) };
        setOptionalField(thinkClose, 'prefix', document.getElementById('swap_reasoning_close_prefix').value);
        setOptionalField(thinkClose, 'suffix', document.getElementById('swap_reasoning_close_suffix').value);
        if (Object.keys(thinkClose).length) {
            cfg.think_close = thinkClose;
        } else {
            delete cfg.think_close;
        }
        base.swap_reasoning_content = cfg;
        delete base.swap_reasoning;
    } else {
        delete base.swap_reasoning_content;
        delete base.swap_reasoning;
    }

    if (currentModulesConfig && currentModulesConfig.raw) {
        if (currentModulesConfig.raw.upstream || currentModulesConfig.raw.downstream) {
            const result = deepClone(currentModulesConfig.raw);
            result.upstream = base;
            return result;
        }
        return base;
    }

    return { upstream: base };
}

async function loadModels() {
    const protectedContainer = document.getElementById('protectedModelList');
    const unprotectedContainer = document.getElementById('unprotectedModelList');
    const treeContainer = document.getElementById('modelTree');
    const loadingMarkup = `
        <div class="loading">
            <div class="spinner"></div>
            <span>Loading models...</span>
        </div>
    `;
    if (protectedContainer) {
        protectedContainer.innerHTML = loadingMarkup;
    }
    if (unprotectedContainer) {
        unprotectedContainer.innerHTML = loadingMarkup;
    }
    if (treeContainer) {
        treeContainer.innerHTML = `
            <div class="loading">
                <div class="spinner"></div>
                <span>Loading model tree...</span>
            </div>
        `;
    }

    try {
        const [payload, tree] = await Promise.all([fetchModels(), fetchModelTree()]);
        renderModels(payload);
        renderModelTree(tree);
    } catch (error) {
        const errorMarkup = `
            <div class="empty-state">
                <div class="empty-icon" style="background: rgba(239, 68, 68, 0.1);">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="color: #ef4444;">
                        <circle cx="12" cy="12" r="10"/>
                        <line x1="12" y1="8" x2="12" y2="12"/>
                        <line x1="12" y1="16" x2="12.01" y2="16"/>
                    </svg>
                </div>
                <h3 class="empty-title">Unable to load models</h3>
                <p class="empty-description">Make sure the proxy server is running and try again</p>
                <button class="btn btn-primary" type="button" data-action="retry-load">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <polyline points="23 4 23 10 17 10"/>
                        <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>
                    </svg>
                    Try Again
                </button>
            </div>
        `;
        if (protectedContainer) {
            protectedContainer.innerHTML = errorMarkup;
        }
        if (unprotectedContainer) {
            unprotectedContainer.innerHTML = errorMarkup;
        }
        if (treeContainer) {
            treeContainer.innerHTML = errorMarkup;
        }
    }
}

async function saveModel(event) {
    event.preventDefault();

    const editName = document.getElementById('editModelName').value;
    const modelData = {
        model_name: document.getElementById('model_name').value.trim(),
        model_params: {
            api_base: document.getElementById('api_base').value.trim(),
            api_key: document.getElementById('api_key').value || '',
            model: document.getElementById('model').value.trim() || undefined,
            request_timeout: parseInt(document.getElementById('request_timeout').value) || undefined,
            api_type: document.getElementById('api_type').value,
            supports_reasoning: document.getElementById('supports_reasoning').checked
        }
    };

    const parameterOverrides = buildParameterOverrides();
    if (parameterOverrides) {
        if (currentParametersSource === 'top') {
            modelData.parameters = parameterOverrides;
        } else {
            modelData.model_params.parameters = parameterOverrides;
        }
    }

    const modulesConfig = buildModulesConfig();
    if (modulesConfig === null) {
        return;
    }
    if (modulesConfig) {
        modelData.modules = modulesConfig;
    }

    // Remove undefined/empty values
    Object.keys(modelData.model_params).forEach(key => {
        const value = modelData.model_params[key];
        if (value === undefined || value === '' || (typeof value === 'string' && !value.trim())) {
            delete modelData.model_params[key];
        }
    });

    try {
        if (editName && editName !== modelData.model_name) {
            await upsertModel(modelData);
            await deleteModel(editName, true);
            showNotification(`Model renamed to "${modelData.model_name}"`, 'success');
        } else {
            const result = await upsertModel(modelData);
            if (editName) {
                showNotification(`Model "${modelData.model_name}" updated`, 'success');
            } else if (result.replaced) {
                showNotification(`Model "${modelData.model_name}" replaced`, 'success');
            } else {
                showNotification(`Model "${modelData.model_name}" added successfully`, 'success');
            }
        }

        closeModal();
        await loadModels();
    } catch (error) {
        console.error('Error saving model:', error);
        showNotification(error.message || 'Failed to save model', 'error');
    }
}

async function upsertModel(modelData) {
    const response = await fetch(`${API_BASE}/models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...getAdminHeaders() },
        body: JSON.stringify(modelData)
    });

    if (!response.ok) {
        const error = await response.json().catch(() => ({}));
        const detail = error.detail || 'Failed to save model';
        if (response.status === 403) {
            openAdminModal();
        }
        throw new Error(detail);
    }

    return response.json();
}

async function deleteModel(modelName, skipConfirm = false) {
    if (!skipConfirm) {
        if (!confirm(`Are you sure you want to delete "${modelName}"?\n\nThis action cannot be undone.`)) {
            return;
        }
    }

    try {
        const response = await fetch(`${API_BASE}/models/${encodeURIComponent(modelName)}`, {
            method: 'DELETE',
            headers: getAdminHeaders()
        });

        if (!response.ok) {
            const error = await response.json().catch(() => ({}));
            const detail = error.detail || 'Failed to delete model';
            if (response.status === 403) {
                openAdminModal();
            }
            throw new Error(detail);
        }

        showNotification(`Model "${modelName}" deleted`, 'success');
        await loadModels();
    } catch (error) {
        console.error('Error deleting model:', error);
        showNotification(error.message || 'Failed to delete model', 'error');
    }
}

async function copyModel(sourceName, targetName) {
    try {
        const response = await fetch(`${API_BASE}/models/copy?source=${encodeURIComponent(sourceName)}&target=${encodeURIComponent(targetName)}`, {
            method: 'POST',
            headers: getAdminHeaders()
        });

        if (!response.ok) {
            const error = await response.json().catch(() => ({}));
            const detail = error.detail || 'Failed to copy model';
            if (response.status === 403) {
                openAdminModal();
            }
            throw new Error(detail);
        }

        const result = await response.json();
        showNotification(`Model "${sourceName}" copied to "${targetName}"`, 'success');
        closeCopyModal();
        await loadModels();
    } catch (error) {
        console.error('Error copying model:', error);
        throw error;
    }
}

async function reloadConfig() {
    try {
        const response = await fetch(`${API_BASE}/config/reload`, {
            method: 'POST',
            headers: getAdminHeaders()
        });

        if (!response.ok) {
            const error = await response.json().catch(() => ({}));
            const detail = error.detail || 'Failed to reload config';
            if (response.status === 403) {
                openAdminModal();
            }
            throw new Error(detail);
        }

        const result = await response.json();
        showNotification(`Configuration reloaded (${result.models_count} models)`, 'success');
        await loadModels();
    } catch (error) {
        console.error('Error reloading config:', error);
        showNotification(error.message || 'Failed to reload config', 'error');
    }
}

function openCopyModal(modelName) {
    document.getElementById('copySourceModel').value = modelName;
    document.getElementById('copySourceName').textContent = modelName;
    document.getElementById('copyTargetName').value = '';

    // Focus target input
    setTimeout(() => {
        document.getElementById('copyTargetName').focus();
    }, 100);

    document.getElementById('copyModal').classList.add('active');
}

function closeCopyModal() {
    document.getElementById('copyModal').classList.remove('active');
}

function openExtendModal(modelName) {
    fetchModels().then(payload => {
        const models = [...(payload?.protected || []), ...(payload?.unprotected || [])];
        const model = models.find(m => m.model_name === modelName);
        if (!model) {
            showNotification('Model not found', 'error');
            return;
        }

        document.getElementById('extendSourceModel').value = modelName;
        document.getElementById('extendSourceName').textContent = modelName;

        // Pre-fill with source model values
        const params = model.model_params || {};
        document.getElementById('extendModelName').value = '';
        document.getElementById('extendApiBase').value = params.api_base || '';
        document.getElementById('extendApiKey').value = '';

        // Focus model name input
        setTimeout(() => {
            document.getElementById('extendModelName').focus();
        }, 100);

        document.getElementById('extendModal').classList.add('active');
    });
}

function closeExtendModal() {
    document.getElementById('extendModal').classList.remove('active');
}

async function saveCopiedModel(event) {
    event.preventDefault();

    const sourceName = document.getElementById('copySourceModel').value;
    const targetName = document.getElementById('copyTargetName').value.trim();

    if (!sourceName || !targetName) {
        showNotification('Please fill in all required fields', 'error');
        return;
    }

    try {
        await copyModel(sourceName, targetName);
    } catch (error) {
        showNotification(error.message || 'Failed to copy model', 'error');
    }
}

async function saveDerivedModel(event) {
    event.preventDefault();

    const sourceName = document.getElementById('extendSourceModel').value;
    const targetName = document.getElementById('extendModelName').value.trim();
    const apiBase = document.getElementById('extendApiBase').value.trim();
    const apiKey = document.getElementById('extendApiKey').value;

    if (!sourceName || !targetName) {
        showNotification('Please fill in all required fields', 'error');
        return;
    }

    try {
        // First copy the model
        const copyResponse = await fetch(`${API_BASE}/models/copy?source=${encodeURIComponent(sourceName)}&target=${encodeURIComponent(targetName)}`, {
            method: 'POST',
            headers: getAdminHeaders()
        });

        if (!copyResponse.ok) {
            const error = await copyResponse.json().catch(() => ({}));
            const detail = error.detail || 'Failed to copy model';
            if (copyResponse.status === 403) {
                openAdminModal();
            }
            throw new Error(detail);
        }

        const copyResult = await copyResponse.json();
        const copiedModel = copyResult.model;

        // If user provided overrides, update the copied model
        if (apiBase || apiKey) {
            const updateData = {
                model_name: targetName,
                model_params: {}
            };

            if (apiBase) {
                updateData.model_params.api_base = apiBase;
            }
            if (apiKey) {
                updateData.model_params.api_key = apiKey;
            }

            await upsertModel(updateData);
        }

        showNotification(`Derived model "${targetName}" created from "${sourceName}"`, 'success');
        closeExtendModal();
        await loadModels();
    } catch (error) {
        console.error('Error creating derived model:', error);
        showNotification(error.message || 'Failed to create derived model', 'error');
    }
}

function openAddModal() {
    document.getElementById('modalTitle').innerHTML = `
        ${Icons.openai}
        Add New Model
    `;
    document.getElementById('editModelName').value = '';
    document.getElementById('modelForm').reset();
    document.getElementById('request_timeout').value = 30;
    document.getElementById('api_type').value = 'openai';
    document.getElementById('supports_reasoning').checked = false;
    currentParametersConfig = {};
    currentParametersSource = 'model_params';
    currentModulesConfig = null;
    resetParametersForm();
    resetModulesForm();

    // Focus first input
    setTimeout(() => {
        document.getElementById('model_name').focus();
    }, 100);

    document.getElementById('modal').classList.add('active');
}

function editModel(modelName) {
    fetchModels().then(payload => {
        const models = [...(payload?.protected || []), ...(payload?.unprotected || [])];
        const model = models.find(m => m.model_name === modelName);
        if (!model) {
            showNotification('Model not found', 'error');
            return;
        }

        const params = model.model_params || {};
        document.getElementById('modalTitle').innerHTML = `
            ${Icons.edit}
            Edit Model
        `;
        document.getElementById('editModelName').value = modelName;
        document.getElementById('model_name').value = model.model_name;
        document.getElementById('api_base').value = params.api_base || '';
        document.getElementById('api_key').value = params.api_key || '';
        document.getElementById('model').value = params.model || '';
        document.getElementById('request_timeout').value = params.request_timeout || 30;
        document.getElementById('api_type').value = params.api_type || 'openai';
        document.getElementById('supports_reasoning').checked = params.supports_reasoning || false;

        const paramInfo = getParameterOverridesInfo(model);
        currentParametersSource = paramInfo.source;
        currentParametersConfig = deepClone(paramInfo.params) || {};
        fillParametersForm(currentParametersConfig);

        const modulesCfgRaw = getModulesConfig(model);
        const hasModules = modulesCfgRaw && Object.keys(modulesCfgRaw).length > 0;
        if (hasModules) {
            const normalized = normalizeModulesConfig(modulesCfgRaw);
            currentModulesConfig = normalized
                ? { raw: deepClone(modulesCfgRaw), upstream: normalized.upstream }
                : null;
        } else {
            currentModulesConfig = null;
        }
        if (currentModulesConfig) {
            fillModulesForm(currentModulesConfig.upstream);
        } else {
            resetModulesForm();
        }

        document.getElementById('modal').classList.add('active');
    });
}

function closeModal() {
    document.getElementById('modal').classList.remove('active');
}

function showNotification(message, type = 'success') {
    const notification = document.getElementById('notification');
    const notificationText = document.getElementById('notificationText');

    notification.className = `notification notification-${type}`;
    notification.innerHTML = `
        ${type === 'success' ? Icons.success : Icons.error}
        <span>${escapeHtml(message)}</span>
    `;

    notification.classList.add('show');

    // Auto-hide after 4 seconds
    clearTimeout(notification.hideTimeout);
    notification.hideTimeout = setTimeout(() => {
        notification.classList.remove('show');
    }, 4000);
}

// Close modal on escape key
document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') {
        closeModal();
        closeCopyModal();
        closeExtendModal();
        closeAdminModal();
    }
});

// Close modal when clicking outside
document.getElementById('modal').addEventListener('click', function(e) {
    if (e.target === this) {
        closeModal();
    }
});

// Form submission
document.getElementById('modelForm').addEventListener('submit', saveModel);

function handleModelListClick(event) {
    const target = event.target.closest('[data-action]');
    if (!target) {
        return;
    }
    if (target.classList.contains('btn-disabled')) {
        return;
    }

    const action = target.dataset.action;
    if (action === 'edit') {
        editModel(target.dataset.model || '');
    } else if (action === 'delete') {
        deleteModel(target.dataset.model || '');
    } else if (action === 'add-model') {
        openAddModal();
    } else if (action === 'copy') {
        openCopyModal(target.dataset.model || '');
    } else if (action === 'retry-load') {
        loadModels();
    }
}

// Template management functions
let availableTemplates = [];

async function loadTemplates() {
    try {
        const response = await fetch(`${API_BASE}/templates`);
        if (!response.ok) {
            console.error('Failed to load templates:', response.statusText);
            return;
        }
        const data = await response.json();
        availableTemplates = data.templates || [];
        populateAllTemplateSelects();
    } catch (error) {
        console.error('Error loading templates:', error);
    }
}

function populateTemplateSelect(selectId, selectedValue = null, placeholderText = '-- Select a template --') {
    const select = document.getElementById(selectId);
    if (!select) return;

    // Preserve current selection if not explicitly setting one
    const currentValue = selectedValue !== null ? selectedValue : select.value;

    // Clear existing options except the placeholder
    select.innerHTML = `<option value="">${placeholderText}</option>`;

    // Add template options
    availableTemplates.forEach(template => {
        const option = document.createElement('option');
        option.value = template.path;
        option.textContent = template.name;
        select.appendChild(option);
    });

    // Restore selection
    if (currentValue) {
        select.value = currentValue;
        // If the value doesn't match any option, it might be a custom path
        // Add it as an option so it's visible
        if (!select.value && currentValue) {
            const customOption = document.createElement('option');
            customOption.value = currentValue;
            customOption.textContent = currentValue + ' (custom)';
            select.appendChild(customOption);
            select.value = currentValue;
        }
    }
}

function populateAllTemplateSelects() {
    populateTemplateSelect('parse_template_path', null, '-- Select a template --');
    populateTemplateSelect('parse_unparsed_template_path', null, '-- None (use manual tags) --');
}

async function uploadTemplate(file, selectId, statusId) {
    const statusEl = document.getElementById(statusId);
    const placeholderText = selectId === 'parse_template_path'
        ? '-- Select a template --'
        : '-- None (use manual tags) --';

    try {
        statusEl.textContent = 'Uploading...';
        statusEl.className = 'upload-status';

        const formData = new FormData();
        formData.append('file', file);

        const response = await fetch(`${API_BASE}/templates`, {
            method: 'POST',
            body: formData
        });

        if (!response.ok) {
            const error = await response.json();
            throw new Error(error.detail || 'Upload failed');
        }

        const result = await response.json();

        // Add to templates list and select it in the target select
        availableTemplates.push(result);
        populateAllTemplateSelects();
        // Select the uploaded template in the triggering select
        populateTemplateSelect(selectId, result.path, placeholderText);

        statusEl.textContent = 'Uploaded!';
        statusEl.className = 'upload-status success';

        // Clear status after 3 seconds
        setTimeout(() => {
            statusEl.textContent = '';
            statusEl.className = 'upload-status';
        }, 3000);

    } catch (error) {
        statusEl.textContent = error.message;
        statusEl.className = 'upload-status error';

        // Clear error after 5 seconds
        setTimeout(() => {
            statusEl.textContent = '';
            statusEl.className = 'upload-status';
        }, 5000);
    }
}

function initTemplateUpload() {
    // Parse Template upload
    const uploadBtn = document.getElementById('template_upload_btn');
    const uploadInput = document.getElementById('template_upload_input');

    if (uploadBtn && uploadInput) {
        uploadBtn.addEventListener('click', () => uploadInput.click());
        uploadInput.addEventListener('change', (e) => {
            if (e.target.files && e.target.files[0]) {
                uploadTemplate(e.target.files[0], 'parse_template_path', 'template_upload_status');
                e.target.value = '';
            }
        });
    }

    // Parse Unparsed upload
    const unparsedUploadBtn = document.getElementById('parse_unparsed_template_upload_btn');
    const unparsedUploadInput = document.getElementById('parse_unparsed_template_upload_input');

    if (unparsedUploadBtn && unparsedUploadInput) {
        unparsedUploadBtn.addEventListener('click', () => unparsedUploadInput.click());
        unparsedUploadInput.addEventListener('change', (e) => {
            if (e.target.files && e.target.files[0]) {
                uploadTemplate(e.target.files[0], 'parse_unparsed_template_path', 'parse_unparsed_template_upload_status');
                e.target.value = '';
            }
        });
    }
}

function initAdminUi() {
    const themeButton = document.querySelector('.theme-toggle');
    if (themeButton) {
        themeButton.addEventListener('click', themeToggle);
    }

    const addButton = document.getElementById('addModelBtn');
    if (addButton) {
        addButton.addEventListener('click', openAddModal);
    }

    const reloadButton = document.getElementById('reloadConfigBtn');
    if (reloadButton) {
        reloadButton.addEventListener('click', reloadConfig);
    }

    const adminAccessBtn = document.getElementById('adminAccessBtn');
    if (adminAccessBtn) {
        adminAccessBtn.addEventListener('click', handleAdminAccessClick);
    }

    document.querySelectorAll('[data-action="close-modal"]').forEach((button) => {
        button.addEventListener('click', closeModal);
    });

    document.querySelectorAll('[data-action="close-admin-modal"]').forEach((button) => {
        button.addEventListener('click', closeAdminModal);
    });
    const adminForm = document.getElementById('adminForm');
    if (adminForm) {
        adminForm.addEventListener('submit', saveAdminAccess);
    }

    // Copy modal handlers
    document.querySelectorAll('[data-action="close-copy-modal"]').forEach((button) => {
        button.addEventListener('click', closeCopyModal);
    });
    document.getElementById('copyModelForm').addEventListener('submit', saveCopiedModel);

    // Extend modal handlers
    document.querySelectorAll('[data-action="close-extend-modal"]').forEach((button) => {
        button.addEventListener('click', closeExtendModal);
    });
    document.getElementById('extendModelForm').addEventListener('submit', saveDerivedModel);

    // Close modals when clicking outside
    document.getElementById('copyModal').addEventListener('click', function(e) {
        if (e.target === this) {
            closeCopyModal();
        }
    });
    document.getElementById('extendModal').addEventListener('click', function(e) {
        if (e.target === this) {
            closeExtendModal();
        }
    });
    document.getElementById('adminModal').addEventListener('click', function(e) {
        if (e.target === this) {
            closeAdminModal();
        }
    });

    const modulesOverride = document.getElementById('modules_override');
    if (modulesOverride) {
        modulesOverride.addEventListener('change', updateModulesVisibility);
    }
    RESPONSE_MODULES.forEach(name => {
        const checkbox = document.getElementById(MODULE_IDS[name]);
        if (checkbox) {
            checkbox.addEventListener('change', updateModulesVisibility);
        }
    });

    ['protectedModelList', 'unprotectedModelList'].forEach((id) => {
        const modelList = document.getElementById(id);
        if (modelList) {
            modelList.addEventListener('click', handleModelListClick);
        }
    });

    updateAdminStatus();
    updateThemeIcons(ThemeManager.getCurrent());
    updateModulesVisibility();
    loadModels();
    loadTemplates();
    initTemplateUpload();
}

// Initialize
document.addEventListener('DOMContentLoaded', initAdminUi);
