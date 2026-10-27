import { useEffect, useState } from "react";
import { errorMessage, requestJSON } from "../lib/api";
import { formatProviderModels, parseProviderModels } from "../lib/routing";
import type { ProviderForm, SelectedProviderModel } from "../types";
import { FieldError } from "./common";
import { Button } from "./ui/button";

export function ProviderModelPicker({ form, adminPassword, disabled, onChange }: {
  form: ProviderForm; adminPassword: string; disabled: boolean; onChange: (text: string) => void;
}) {
  const [discovered, setDiscovered] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [fetched, setFetched] = useState(false);
  useEffect(() => { setDiscovered([]); setFetched(false); setError(""); }, [form.name, form.api_base, form.api_type]);
  let selected: SelectedProviderModel[] = [];
  try { selected = parseProviderModels(form.models_text); } catch { /* Form validation displays invalid manual selections. */ }
  async function discover() {
    setBusy(true); setError("");
    try {
      const result = await requestJSON<{models: string[]}>("/admin/providers/discover", { method: "POST", body: JSON.stringify({ name: form.name, api_base: form.api_base, api_type: form.api_type, api_key: form.api_key }) }, adminPassword);
      setDiscovered(result.models); setFetched(true);
    } catch (err) { setError(errorMessage(err)); } finally { setBusy(false); }
  }
  return <div className="field routing-nested-field">
    <label className="field">
      <span>Models to expose</span>
      <textarea className="routing-list-textarea" value={form.models_text} onChange={(event) => onChange(event.target.value)} placeholder={"qwen\nllama\npublic-name = upstream-name"} />
    </label>
    <span>One model per line. Use public-name = upstream-name for an alias. Saving creates the model pools automatically.</span>
    <Button type="button" variant="secondary" disabled={disabled || busy || !form.api_base} onClick={() => void discover()}>{busy ? "Fetching…" : "Fetch available models"}</Button>
    <FieldError message={error} />
    {fetched && <>
      <span>Choose models to expose, then save. Discovery does not change your selection.</span>
      <Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(formatProviderModels([...selected, ...discovered.filter((id) => !selected.some((model) => (model.upstream_model || model.name) === id || model.name === id)).map((name) => ({name}))]))}>Select all discovered</Button>
      <div className="choice-list provider-model-options">{discovered.map((id) => <label key={id} className="choice-item">
        <input type="checkbox" checked={selected.some((model) => (model.upstream_model || model.name) === id)} onChange={(event) => onChange(formatProviderModels(event.target.checked ? [...selected.filter((model) => model.name !== id), {name: id}] : selected.filter((model) => (model.upstream_model || model.name) !== id)))} />
        <span>{id}</span>
      </label>)}</div>
      {discovered.length === 0 && <span>The provider returned no models.</span>}
    </>}
  </div>;
}
