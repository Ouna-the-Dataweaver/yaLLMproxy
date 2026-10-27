import { FileJson, KeyRound, Plus, RefreshCw, Save, X } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, FieldError, PanelTitle, SecretBanner } from "../components/common";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { appKeyGroups, keyIDFromRecord, renderAppKeyCell } from "../lib/appKeys";
import type { JsonRecord } from "../types";

export function AppKeysTab() {
  const { appKeyConfigErrors, appKeyConfigForm, appKeyEdit, appKeyErrors, appKeyForm, appKeyGroupIDs, appKeys, appKeysConfig, cancelAppKeyEdit, createAppKey, deleteAppKey, loadAppKeys, loading, regenerateAppKey, saveAppKeysConfig, secretReveal, setAppKeyConfigForm, setAppKeyEdit, setAppKeyForm, setSecretReveal, startAppKeyEdit, updateAppKey } = useAdmin();
  return (
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
  );
}
