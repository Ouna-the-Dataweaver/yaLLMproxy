import { ListFilter, RefreshCw } from "lucide-react";
import { useAdmin } from "../admin/context";
import { DataTable, PanelTitle } from "../components/common";
import { CatalogStatus, CatalogWarnings } from "../components/StatusNotices";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { formatTime } from "../lib/format";
import { catalogTableEmpty, renderCatalogCell } from "../lib/observability";
import type { CatalogRow } from "../types";

export function CatalogTab() {
  const { catalog, catalogError, catalogLoading, catalogRows, catalogStatus, catalogWarnings, loadCatalog } = useAdmin();
  return (
    <div className="catalog-view">
      <section className="panel wide">
        <div className="panel-header">
          <PanelTitle icon={ListFilter} title="Model Catalog" />
          <div className="toolbar">
            {catalogLoading && <Badge tone="warn">loading</Badge>}
            <Badge tone={catalog?.enabled ? "good" : "neutral"}>{catalog?.enabled ? "enabled" : "disabled"}</Badge>
            <Badge tone={catalog?.probe_enabled ? "good" : "neutral"}>{catalog?.probe_enabled ? "probes on" : "probes off"}</Badge>
            <Badge>{catalogRows.length} nodes</Badge>
            <Button variant="secondary" size="icon" onClick={() => void loadCatalog()} title="Reload catalog">
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </div>
        <CatalogStatus error={catalogError} status={catalogStatus} snapshot={catalog} loading={catalogLoading} />
        <div className="catalog-meta">
          <span>Generated</span>
          <strong>{formatTime(catalog?.generated_at)}</strong>
        </div>
        <CatalogWarnings warnings={catalogWarnings} />
        <DataTable
          columns={["model", "provider", "node", "status", "source", "error_class", "configured_model", "target_model", "last_sync", "next_sync"]}
          rows={catalogRows}
          empty={catalogTableEmpty(catalog, catalogError, catalogStatus, catalogLoading)}
          renderCell={(row, column) => renderCatalogCell(row as CatalogRow, column)}
        />
      </section>
    </div>
  );
}
