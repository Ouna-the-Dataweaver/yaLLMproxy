import { KeyRound, Save, Server } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, FieldError, PanelTitle } from "../components/common";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { formatValue } from "../lib/format";
import { renderBooleanBadge } from "../lib/logs";
import type { JsonRecord } from "../types";

export function LegacyModelsTab() {
  const { loading, modelErrors, modelForm, models, registerModel, setModelForm } = useAdmin();
  return (
    <div className="models-view">
      <section className="panel wide">
        <div className="panel-header">
          <PanelTitle icon={Server} title="Configured Models" />
          <Badge>{models.length} models</Badge>
        </div>
        <DataTable
          columns={["id", "supports_reasoning", "tags"]}
          rows={models as JsonRecord[]}
          empty="No configured models"
          renderCell={(row, column) => column === "supports_reasoning" ? renderBooleanBadge(row[column]) : formatValue(row[column])}
        />
      </section>

      <section className="panel model-panel">
        <PanelTitle icon={KeyRound} title="Register Model" />
        <form className="model-form" onSubmit={(event) => void registerModel(event)}>
          <label className="field">
            <span>Model name</span>
            <input required value={modelForm.model_name} onChange={(event) => setModelForm({ ...modelForm, model_name: event.target.value })} />
            <FieldError message={modelErrors.model_name} />
          </label>
          <label className="field">
            <span>API base</span>
            <input required value={modelForm.api_base} onChange={(event) => setModelForm({ ...modelForm, api_base: event.target.value })} />
            <FieldError message={modelErrors.api_base} />
          </label>
          <label className="field">
            <span>Upstream model</span>
            <input value={modelForm.model} onChange={(event) => setModelForm({ ...modelForm, model: event.target.value })} />
          </label>
          <label className="field">
            <span>API key</span>
            <input type="password" value={modelForm.api_key} onChange={(event) => setModelForm({ ...modelForm, api_key: event.target.value })} />
          </label>
          <label className="field">
            <span>API type</span>
            <select value={modelForm.api_type} onChange={(event) => setModelForm({ ...modelForm, api_type: event.target.value })}>
              <option value="openai">openai</option>
              <option value="anthropic">anthropic</option>
              <option value="gigachat">gigachat</option>
              <option value="passthrough">passthrough</option>
            </select>
            <FieldError message={modelErrors.api_type} />
          </label>
          {modelForm.api_type === "gigachat" && (
            <>
              <label className="field">
                <span>Mode</span>
                <select value={modelForm.mode} onChange={(event) => setModelForm({ ...modelForm, mode: event.target.value })}>
                  <option value="">auto</option>
                  <option value="cloud">cloud</option>
                  <option value="local">local</option>
                </select>
              </label>
              <label className="field">
                <span>Scope</span>
                <input value={modelForm.scope} placeholder="GIGACHAT_API_CORP" onChange={(event) => setModelForm({ ...modelForm, scope: event.target.value })} />
              </label>
              <label className="field">
                <span>Auth URL</span>
                <input value={modelForm.auth_url} placeholder="https://ngw.devices.sberbank.ru:9443/api/v2/oauth" onChange={(event) => setModelForm({ ...modelForm, auth_url: event.target.value })} />
                <FieldError message={modelErrors.auth_url} />
              </label>
              <label className="field">
                <span>Client ID</span>
                <input value={modelForm.client_id} onChange={(event) => setModelForm({ ...modelForm, client_id: event.target.value })} />
              </label>
              <label className="field">
                <span>Client secret</span>
                <input type="password" value={modelForm.client_secret} onChange={(event) => setModelForm({ ...modelForm, client_secret: event.target.value })} />
                <FieldError message={modelErrors.client_secret} />
              </label>
              <label className="field">
                <span>Client cert</span>
                <input value={modelForm.client_cert} onChange={(event) => setModelForm({ ...modelForm, client_cert: event.target.value })} />
                <FieldError message={modelErrors.client_cert} />
              </label>
              <label className="field">
                <span>Client key</span>
                <input value={modelForm.client_key} onChange={(event) => setModelForm({ ...modelForm, client_key: event.target.value })} />
              </label>
              <label className="field checkbox-field">
                <input type="checkbox" checked={modelForm.verify_ssl} onChange={(event) => setModelForm({ ...modelForm, verify_ssl: event.target.checked })} />
                <span>Verify SSL</span>
              </label>
              <label className="field checkbox-field">
                <input type="checkbox" checked={modelForm.emulate_tool_calls} onChange={(event) => setModelForm({ ...modelForm, emulate_tool_calls: event.target.checked })} />
                <span>Emulate tool calls</span>
              </label>
            </>
          )}
          <label className="field">
            <span>Fallbacks</span>
            <input value={modelForm.fallbacks} onChange={(event) => setModelForm({ ...modelForm, fallbacks: event.target.value })} />
          </label>
          <div className="form-actions">
            <Button type="submit" disabled={loading}>
              <Save size={16} aria-hidden="true" />
              Save
            </Button>
          </div>
        </form>
      </section>
    </div>
  );
}
