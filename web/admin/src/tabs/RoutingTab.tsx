import { Copy, KeyRound, Network, Plus, RefreshCw, Save, Server, Trash2, X } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, FieldError, PanelTitle } from "../components/common";
import { ProviderModelPicker } from "../components/ProviderModelPicker";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { errorMessage } from "../lib/api";
import { asArray, formatValue, toggleStringSelection } from "../lib/format";
import { modelTreeRows, renderClientModelCell, renderModelTreeCell, renderNodeCell, renderProviderCell, renderStringBadges } from "../lib/routing";
import type { JsonRecord } from "../types";

export function RoutingTab() {
  const { addFallbackRow, adminPassword, cancelModelCopy, cancelRoutingEdit, clientModelErrors, clientModelForm, clientModels, copyLegacyModel, copyModelForm, createClientModel, createNode, createProvider, deleteClientModel, deleteLegacyModel, deleteNode, deleteProvider, dependencyError, dependencyModel, fallbackError, fallbackModelNames, legacyModelNames, loadModelDependencies, loading, modelAncestry, modelDependents, modelTree, newFallback, nodeErrors, nodeForm, nodeNames, nodes, prefixAffinity, providerErrors, providerForm, providerNames, providers, removeFallbackRow, routerFallbacks, routingConflict, routingEdit, routingPools, saveRouterFallbacks, setClientModelForm, setCopyModelForm, setDependencyError, setDependencyModel, setFallbackError, setNewFallback, setNodeForm, setProviderForm, setRoutingEdit, startClientModelEdit, startModelCopy, startNodeEdit, startProviderEdit, toggleFallbackTarget, updateClientModel, updateFallbackSource, updateNode, updateProvider } = useAdmin();
  return (
    <div className="routing-view">
      <section className="panel">
        <PanelTitle icon={Network} title="Models and nodes" />
        <p>Add a provider and select its models, or add a node directly. Nodes with the same public model name share a pool.</p>
        <p>{prefixAffinity ? "Conversation and prompt-cache affinity take priority; otherwise requests are balanced across eligible nodes." : "Prompt-cache affinity is disabled in router settings. Conversation affinity and balancing still apply."} Retryable failures try another node before any model fallback.</p>
        <DataTable columns={["model", "nodes"]} rows={routingPools} empty="No model pools configured"
          renderCell={(row, column) => column === "model" ? formatValue(row.name) : (
            <div className="node-list">{asArray<JsonRecord>(row.nodes).map((node) => (
              <span key={String(node.name)}><Badge tone={node.healthy ? "good" : "bad"}>{node.healthy ? "Available" : "Unhealthy"}</Badge> {String(node.name)} → {String(node.upstream_model || row.name)}</span>
            ))}</div>
          )} />
      </section>
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
            columns={["name", "api_base", "api_type", "models", "actions"]}
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
              <ProviderModelPicker form={routingEdit.form} adminPassword={adminPassword} disabled={loading}
                onChange={(models_text) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, models_text } })} />
              <FieldError message={routingEdit.errors.models_text} />
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
            <ProviderModelPicker form={providerForm} adminPassword={adminPassword} disabled={loading}
              onChange={(models_text) => setProviderForm({ ...providerForm, models_text })} />
            <FieldError message={providerErrors.models_text} />
            <label className="field">
              <span>Limits per upstream model (JSON, optional)</span>
              <textarea value={providerForm.limits_text} onChange={(event) => setProviderForm({ ...providerForm, limits_text: event.target.value })} />
              <FieldError message={providerErrors.limits_text} />
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
                  value={routingEdit.form.provider}
                  onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, provider: event.target.value } })}
                >
                  <option value="">Direct connection</option>
                  {providerNames.map((name) => (
                    <option key={name} value={name}>{name}</option>
                  ))}
                </select>
                <FieldError message={routingEdit.errors.provider} />
              </label>
              <label className="field">
                <span>Model name</span>
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
              <label className="field">
                <span>Upstream model name (if different)</span>
                <input value={routingEdit.form.upstream_model} onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, upstream_model: event.target.value } })} />
              </label>
              <label className="field">
                <span>Replace API key (optional)</span>
                <input type="password" autoComplete="off" value={routingEdit.form.api_key} onChange={(event) => setRoutingEdit({ ...routingEdit, form: { ...routingEdit.form, api_key: event.target.value } })} />
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
                <Button type="submit" disabled={loading}>
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
              <select value={nodeForm.provider} onChange={(event) => setNodeForm({ ...nodeForm, provider: event.target.value })}>
                <option value="">Direct connection</option>
                {providerNames.map((name) => (
                  <option key={name} value={name}>{name}</option>
                ))}
              </select>
              <FieldError message={nodeErrors.provider} />
            </label>
            <label className="field">
              <span>Public model name</span>
              <input required value={nodeForm.model} onChange={(event) => setNodeForm({ ...nodeForm, model: event.target.value })} />
              <FieldError message={nodeErrors.model} />
            </label>
            <label className="field">
              <span>Upstream model name (if different)</span>
              <input value={nodeForm.upstream_model} onChange={(event) => setNodeForm({ ...nodeForm, upstream_model: event.target.value })} />
            </label>
            <label className="field">
              <span>API key (optional)</span>
              <input type="password" autoComplete="off" value={nodeForm.api_key} onChange={(event) => setNodeForm({ ...nodeForm, api_key: event.target.value })} />
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
              <Button type="submit" disabled={loading}>
                <Plus size={16} aria-hidden="true" />
                Create
              </Button>
            </div>
          </form>
        </section>
      </section>

      <details>
        <summary>Advanced: explicit model pools</summary>
        <p>Existing explicit pools remain supported. New nodes and provider selections form pools automatically.</p>
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

      </details>

      <details>
        <summary>Optional: fallback to a different model</summary>
      <section className="routing-block">
        <section className="panel routing-list-panel">
          <div className="panel-header">
            <PanelTitle icon={Network} title="Optional: fallback to a different model" />
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

      </details>
      <details>
        <summary>Legacy model inheritance</summary>
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
      </details>
    </div>
  );
}
