import { CheckCircle2, FileJson, RefreshCw, Save, UploadCloud } from "lucide-react";
import { useAdmin } from "../admin/context";
import { PanelTitle, ValidationIssues } from "../components/common";
import { Button } from "../components/ui/button";
import { errorMessage } from "../lib/api";

export function ConfigTab() {
  const { configIssues, configText, loadConfig, loading, reloadConfig, saveConfig, setConfigText, setError, validateConfigEditor, validationSummary } = useAdmin();
  return (
    <section className="panel editor-panel">
      <div className="panel-header">
        <PanelTitle icon={FileJson} title="Raw Config" />
        <div className="toolbar">
          <Button variant="secondary" size="icon" onClick={() => void loadConfig()} title="Reload editor">
            <RefreshCw size={17} aria-hidden="true" />
          </Button>
          <Button variant="secondary" size="icon" onClick={() => void validateConfigEditor().catch((err) => setError(errorMessage(err)))} title="Validate config">
            <CheckCircle2 size={17} aria-hidden="true" />
          </Button>
          <Button variant="secondary" size="icon" onClick={() => void reloadConfig()} title="Reload from disk">
            <UploadCloud size={17} aria-hidden="true" />
          </Button>
          <Button size="icon" onClick={() => void saveConfig()} disabled={loading} title="Save config">
            <Save size={17} aria-hidden="true" />
          </Button>
        </div>
      </div>
      <textarea className="config-editor" value={configText} onChange={(event) => setConfigText(event.target.value)} spellCheck={false} />
      <ValidationIssues issues={configIssues} summary={validationSummary} />
    </section>
  );
}
