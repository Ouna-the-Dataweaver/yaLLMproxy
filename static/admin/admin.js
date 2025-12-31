const API_BASE = '/admin';

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

function renderModels(payload) {
    const defaultModels = payload?.default || [];
    const addedModels = payload?.added || [];

    const defaultCountEl = document.getElementById('defaultCount');
    const addedCountEl = document.getElementById('addedCount');
    if (defaultCountEl) {
        defaultCountEl.textContent = defaultModels.length;
    }
    if (addedCountEl) {
        addedCountEl.textContent = addedModels.length;
    }

    renderModelList('defaultModelList', defaultModels, {
        title: 'No default models configured',
        description: 'Add models to config_default.yaml to show them here.',
        showAdd: false,
        transparentIcon: false
    });
    renderModelList('addedModelList', addedModels, {
        title: 'No added models yet',
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

function renderModelCards(models) {
    return models.map((model, index) => {
        const params = model.model_params || {};
        const apiType = params.api_type || 'openai';
        const supportsReasoning = params.supports_reasoning;
        const editable = model.editable !== false; // Default to true if not specified
        const safeModelName = escapeHtml(model.model_name);
        const source = model.source || (editable ? 'added' : 'default');
        const sourceLabel = source === 'added' ? 'Added' : 'Default';

        let badges = '';
        if (apiType === 'openai') {
            badges += `<span class="badge badge-openai">${Icons.openai}OpenAI</span>`;
        } else if (apiType === 'anthropic') {
            badges += `<span class="badge badge-anthropic">${Icons.anthropic}Anthropic</span>`;
        }
        if (supportsReasoning) {
            badges += `<span class="badge badge-reasoning">${Icons.reasoning}Reasoning</span>`;
        }
        
        // Add editable/locked badge
        if (editable) {
            badges += `<span class="badge badge-editable">${sourceLabel}</span>`;
        } else {
            badges += `<span class="badge badge-locked">${sourceLabel}</span>`;
        }

        const details = [];
        details.push({ label: 'API Endpoint', value: params.api_base });
        if (params.model) {
            details.push({ label: 'Upstream Model', value: params.model });
        }
        if (params.api_key) {
            details.push({ label: 'API Key', value: params.api_key });
        }
        details.push({ label: 'Timeout', value: `${params.request_timeout || 'Default'}s` });

        // Build action buttons with disabled state for non-editable models
        const editDisabled = !editable ? 'btn-disabled tooltip' : '';
        const deleteDisabled = !editable ? 'btn-disabled tooltip' : '';
        const editTooltip = !editable ? 'data-tooltip="Config-loaded models cannot be modified"' : '';
        const deleteTooltip = !editable ? 'data-tooltip="Config-loaded models cannot be deleted"' : '';
        const editAria = !editable ? 'aria-disabled="true"' : 'aria-disabled="false"';
        const deleteAria = !editable ? 'aria-disabled="true"' : 'aria-disabled="false"';

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
                    <button class="btn btn-secondary btn-icon ${editDisabled}"
                            type="button"
                            data-action="edit"
                            data-model="${safeModelName}"
                            ${editTooltip}
                            ${editAria}
                            title="${editable ? 'Edit model' : 'Config-loaded models cannot be modified'}">
                        ${Icons.edit}
                    </button>
                    <button class="btn btn-danger btn-icon ${deleteDisabled}"
                            type="button"
                            data-action="delete"
                            data-model="${safeModelName}"
                            ${deleteTooltip}
                            ${deleteAria}
                            title="${editable ? 'Delete model' : 'Config-loaded models cannot be deleted'}">
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

async function loadModels() {
    const defaultContainer = document.getElementById('defaultModelList');
    const addedContainer = document.getElementById('addedModelList');
    const loadingMarkup = `
        <div class="loading">
            <div class="spinner"></div>
            <span>Loading models...</span>
        </div>
    `;
    if (defaultContainer) {
        defaultContainer.innerHTML = loadingMarkup;
    }
    if (addedContainer) {
        addedContainer.innerHTML = loadingMarkup;
    }

    try {
        const payload = await fetchModels();
        renderModels(payload);
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
        if (defaultContainer) {
            defaultContainer.innerHTML = errorMarkup;
        }
        if (addedContainer) {
            addedContainer.innerHTML = errorMarkup;
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
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(modelData)
    });

    if (!response.ok) {
        const error = await response.json();
        throw new Error(error.detail || 'Failed to save model');
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
            method: 'DELETE'
        });

        if (!response.ok) {
            const error = await response.json();
            throw new Error(error.detail || 'Failed to delete model');
        }

        showNotification(`Model "${modelName}" deleted`, 'success');
        await loadModels();
    } catch (error) {
        console.error('Error deleting model:', error);
        showNotification(error.message || 'Failed to delete model', 'error');
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

    // Focus first input
    setTimeout(() => {
        document.getElementById('model_name').focus();
    }, 100);

    document.getElementById('modal').classList.add('active');
}

function editModel(modelName) {
    fetchModels().then(payload => {
        const models = [...(payload?.default || []), ...(payload?.added || [])];
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
    } else if (action === 'retry-load') {
        loadModels();
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

    document.querySelectorAll('[data-action="close-modal"]').forEach((button) => {
        button.addEventListener('click', closeModal);
    });

    ['defaultModelList', 'addedModelList'].forEach((id) => {
        const modelList = document.getElementById(id);
        if (modelList) {
            modelList.addEventListener('click', handleModelListClick);
        }
    });

    updateThemeIcons(ThemeManager.getCurrent());
    loadModels();
}

// Initialize
document.addEventListener('DOMContentLoaded', initAdminUi);
