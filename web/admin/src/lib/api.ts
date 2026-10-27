import type { AdminPasswordStatusTone } from "../types";
import { asArray, isJsonRecord, isNonEmptyString, titleCase } from "./format";

export async function requestJSON<T>(path: string, init?: RequestInit, adminPassword?: string): Promise<T> {
  const headers = new Headers(init?.headers);
  if (adminPassword) {
    headers.set("x-admin-password", adminPassword);
  }
  if (init?.body && !headers.has("content-type")) {
    headers.set("content-type", "application/json");
  }
  const response = await fetch(path, { ...init, headers });
  const text = await response.text();
  const payload = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new APIError(String(payload.error?.message || payload.message || payload.error || response.statusText), response.status, payload);
  }
  return payload as T;
}

export class APIError extends Error {
  status: number;
  payload: unknown;

  constructor(message: string, status: number, payload: unknown) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.payload = payload;
  }
}

export function isAdminPasswordAuthError(err: unknown) {
  return err instanceof APIError && (err.status === 401 || err.status === 403);
}

export function adminPasswordVerificationMessage(err: unknown) {
  if (isAdminPasswordAuthError(err)) return "Admin password is invalid";
  return `Could not verify admin password: ${errorMessage(err)}`;
}

export function adminPasswordStatusText(input: string, applied: string, verified: boolean, checking: boolean) {
  if (checking) return "checking";
  if (!input && !applied) return "locked";
  if (input !== applied) return "not applied";
  if (verified) return "verified";
  return "unverified";
}

export function adminPasswordButtonLabel(status: string) {
  if (status === "not applied") return "Admin pending";
  if (status === "checking") return "Admin checking";
  return `Admin ${status}`;
}

export function adminPasswordStatusClass(input: string, applied: string, verified: boolean, checking: boolean): AdminPasswordStatusTone {
  if (checking) return "warn";
  if (!input && !applied) return "neutral";
  if (input !== applied) return "warn";
  if (verified) return "good";
  return "neutral";
}

export function apiErrorPayload(err: unknown) {
  return err instanceof APIError ? err.payload : undefined;
}

export function deleteConflictMessage(err: unknown, label: string, name: string) {
  if (err instanceof APIError && err.status === 409) {
    const payload = apiErrorPayload(err);
    const code = payloadErrorCode(payload);
    const references = payloadReferences(payload);
    const referenceText = references.length > 0 ? `: ${references.join(", ")}` : "";
    if (code === "provider_has_nodes") {
      return `Provider "${name}" is still referenced by nodes${referenceText}`;
    }
    if (code === "node_has_client_models") {
      return `Node "${name}" is still referenced by client models${referenceText}`;
    }
    return `${titleCase(label)} "${name}" is still referenced${referenceText}`;
  }
  return errorMessage(err);
}

export function payloadErrorCode(payload: unknown) {
  if (!isJsonRecord(payload) || !isJsonRecord(payload.error)) return "";
  return typeof payload.error.code === "string" ? payload.error.code : "";
}

export function payloadReferences(payload: unknown) {
  if (!isJsonRecord(payload)) return [];
  return asArray<unknown>(payload.references).filter(isNonEmptyString);
}

export function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : String(err);
}
