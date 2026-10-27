import { CheckCircle2, Monitor, Moon, RefreshCw, Shield, Sun, X } from "lucide-react";
import { AdminContext } from "./admin/context";
import { useAdminController } from "./admin/useAdminController";
import { Button } from "./components/ui/button";
import { statusLine, tabGroups, tabs } from "./lib/navigation";
import { useThemePreference } from "./lib/theme";
import { AppKeysTab } from "./tabs/AppKeysTab";
import { CatalogTab } from "./tabs/CatalogTab";
import { ConfigTab } from "./tabs/ConfigTab";
import { LegacyModelsTab } from "./tabs/LegacyModelsTab";
import { LogsTab } from "./tabs/LogsTab";
import { ObservabilityTab } from "./tabs/ObservabilityTab";
import { OverviewTab } from "./tabs/OverviewTab";
import { RoutingTab } from "./tabs/RoutingTab";
import { TracesTab } from "./tabs/TracesTab";

export function AdminApp() {
  const admin = useAdminController();
  const { preference: themePreference, cycle: cycleTheme } = useThemePreference();
  const ThemeIcon = themePreference === "dark" ? Moon : themePreference === "light" ? Sun : Monitor;
  const { activeTab, adminPassword, adminPasswordButtonText, adminPasswordControlRef, adminPasswordFeedback, adminPasswordInput, adminPasswordPopoverOpen, adminPasswordStatus, adminPasswordStatusTone, adminPasswordSubmitDisabled, clearAdminPassword, confirmAdminPassword, error, health, healthyCount, loading, logMeta, notice, refreshAll, setAdminPasswordFeedback, setAdminPasswordInput, setAdminPasswordPopoverOpen, setError, setNotice, switchTab } = admin;
  return (
    <AdminContext.Provider value={admin}>
      <main className="shell">
        <aside className="sidebar">
          <div className="brand">
            <div className="brand-mark">
              <Shield size={17} aria-hidden="true" />
            </div>
            <div>
              <h1>yaLLMproxy</h1>
              <p>Admin console</p>
            </div>
          </div>
          <nav className="nav" aria-label="Admin sections">
            {tabGroups.map((group) => (
              <div className="nav-group" key={group}>
                <span className="nav-group-label">{group}</span>
                {tabs.filter((tab) => tab.group === group).map((tab) => {
                  const Icon = tab.icon;
                  return (
                    <button key={tab.id} className={activeTab === tab.id ? "nav-item active" : "nav-item"} onClick={() => switchTab(tab.id)} aria-current={activeTab === tab.id ? "page" : undefined}>
                      <Icon size={16} aria-hidden="true" />
                      <span>{tab.label}</span>
                    </button>
                  );
                })}
              </div>
            ))}
          </nav>
          <div className="sidebar-footer">
            <button type="button" className="nav-item" onClick={cycleTheme} title="Switch color theme">
              <ThemeIcon size={16} aria-hidden="true" />
              <span>Theme: {themePreference}</span>
            </button>
          </div>
        </aside>

        <section className="workspace">
          <header className="topbar">
            <div>
              <h2>{tabs.find((tab) => tab.id === activeTab)?.label}</h2>
              <p>{statusLine(health.length, healthyCount, logMeta.total)}</p>
            </div>
            <div className="toolbar">
              <div className="admin-password-control" ref={adminPasswordControlRef}>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  className={`admin-password-trigger admin-password-trigger-${adminPasswordStatusTone}`}
                  onClick={() => {
                    setAdminPasswordPopoverOpen((open) => !open);
                    setAdminPasswordFeedback(null);
                  }}
                  aria-expanded={adminPasswordPopoverOpen}
                  aria-controls="admin-password-popover"
                  title="Admin access"
                >
                  <Shield size={15} aria-hidden="true" />
                  {adminPasswordButtonText}
                </Button>
                {adminPasswordPopoverOpen && (
                  <div id="admin-password-popover" className="admin-password-popover" role="dialog" aria-label="Admin access">
                    <form className="admin-password-form" onSubmit={confirmAdminPassword}>
                      <label className="field admin-password-field">
                        <span>Admin password</span>
                        <input
                          type="password"
                          value={adminPasswordInput}
                          onChange={(event) => {
                            setAdminPasswordInput(event.target.value);
                            setAdminPasswordFeedback(null);
                            setNotice("");
                            setError("");
                          }}
                          autoComplete="current-password"
                          autoFocus
                        />
                      </label>
                      <Button type="submit" size="sm" disabled={adminPasswordSubmitDisabled} title="Verify admin password">
                        <CheckCircle2 size={15} aria-hidden="true" />
                        Verify
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="admin-password-clear"
                        onClick={clearAdminPassword}
                        disabled={!adminPassword && !adminPasswordInput}
                        title="Clear admin password"
                      >
                        <X size={15} aria-hidden="true" />
                      </Button>
                      <span
                        className={`admin-password-status admin-password-status-${adminPasswordFeedback?.tone || adminPasswordStatusTone}`}
                        aria-live="polite"
                      >
                        {adminPasswordFeedback?.text || adminPasswordStatus}
                      </span>
                    </form>
                  </div>
                )}
              </div>
              {(error || notice) && (
                <span className={error ? "topbar-status topbar-status-bad" : "topbar-status topbar-status-good"} role={error ? "alert" : "status"}>
                  {error || notice}
                </span>
              )}
              <Button variant="secondary" size="icon" onClick={() => void refreshAll()} disabled={loading} title="Refresh">
                <RefreshCw size={16} aria-hidden="true" className={loading ? "spin" : undefined} />
              </Button>
            </div>
          </header>

          {activeTab === "overview" && <OverviewTab />}

          {activeTab === "observability" && <ObservabilityTab />}

          {activeTab === "catalog" && <CatalogTab />}

          {activeTab === "traces" && <TracesTab />}

          {activeTab === "logs" && <LogsTab />}

          {activeTab === "config" && <ConfigTab />}

          {activeTab === "models" && <LegacyModelsTab />}

          {activeTab === "routing" && <RoutingTab />}

          {activeTab === "keys" && <AppKeysTab />}
        </section>
      </main>
    </AdminContext.Provider>
  );
}
