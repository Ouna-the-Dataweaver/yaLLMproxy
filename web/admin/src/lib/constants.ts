import type { AppKeyConfigForm, AppKeyForm, CopyModelForm, LogFilters } from "../types";

export const adminPasswordStorageKey = "yallmp.adminPassword";

export const logPageSize = 50;

export const emptyLogFilters: LogFilters = {
  model: "",
  outcome: "",
  stopReason: "",
  toolCall: "",
  startDate: "",
  endDate: "",
  search: ""
};

export function readStoredAdminPassword() {
  try {
    // Never keep an admin credential across browser sessions. Remove values
    // written by older builds while retaining same-tab reload convenience.
    localStorage.removeItem(adminPasswordStorageKey);
    return sessionStorage.getItem(adminPasswordStorageKey) || "";
  } catch {
    return "";
  }
}

export const emptyAppKeyForm: AppKeyForm = {
  key_id: "",
  group_id: "",
  name: "",
  description: "",
  enabled: true,
  models_text: "",
  concurrency_limit: "",
  priority: "",
  queue_timeout: "",
  limits_text: "{}",
  required_headers_text: "[]"
};

export const emptyAppKeyConfigForm: AppKeyConfigForm = {
  enabled: false,
  header_name: "x-app-key",
  allow_unauthenticated: false,
  defaults_text: "{}",
  unauthenticated_text: "{}",
  groups_text: "[]",
  required_headers_text: "[]"
};

export const emptyCopyModelForm: CopyModelForm = {
  source: "",
  target: "",
  error: ""
};
