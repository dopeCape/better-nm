// bnmd API client over the shell's `api_request`. Non-2xx answers become ApiError
// with the daemon's {error, hint, code}; transport failures become
// DaemonUnreachableError so the banner can tell them apart from real errors.
import { shell, isUnreachable } from "@/shell";
import type { ApiErrorBody, ApiErrorCode } from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode | string;
  readonly hint?: string;
  constructor(status: number, body: ApiErrorBody) {
    super(body.error || `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code ?? codeFromStatus(status);
    if (body.hint) this.hint = body.hint;
  }
}

export class DaemonUnreachableError extends Error {
  constructor(detail: string) {
    super(detail);
    this.name = "DaemonUnreachableError";
  }
}

function codeFromStatus(status: number): ApiErrorCode {
  switch (status) {
    case 400:
      return "invalid";
    case 403:
      return "permission";
    case 404:
      return "not-found";
    case 409:
      return "conflict";
    case 501:
      return "unsupported";
    case 503:
      return "unavailable";
    default:
      return "internal";
  }
}

/** Turns a raw daemon body into an ApiError, tolerating non-JSON bodies. */
export function parseError(status: number, body: string): ApiError {
  let parsed: ApiErrorBody;
  try {
    const j = JSON.parse(body) as Partial<ApiErrorBody>;
    parsed = { error: typeof j.error === "string" && j.error ? j.error : body || `HTTP ${status}`, ...(j.hint ? { hint: j.hint } : {}), ...(j.code ? { code: j.code } : {}) };
  } catch {
    parsed = { error: body.trim() || `HTTP ${status}` };
  }
  return new ApiError(status, parsed);
}

async function request<T>(method: "GET" | "POST" | "PUT" | "DELETE", path: string, body?: unknown): Promise<T> {
  let res;
  try {
    res = await shell.apiRequest(method, path, body);
  } catch (e) {
    const msg = typeof e === "string" ? e : e instanceof Error ? e.message : String(e);
    if (isUnreachable(msg)) throw new DaemonUnreachableError(msg);
    throw new DaemonUnreachableError(`daemon-unreachable: ${msg}`);
  }
  if (res.status < 200 || res.status >= 300) throw parseError(res.status, res.body);
  if (!res.body) return undefined as T;
  return JSON.parse(res.body) as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T = { ok: true }>(path: string, body?: unknown) => request<T>("POST", path, body ?? {}),
  put: <T>(path: string, body: unknown) => request<T>("PUT", path, body),
  del: <T = { ok: true }>(path: string) => request<T>("DELETE", path),
};

/** Human message for a toast: the error line, plus the hint when the daemon gave one. */
export function describeError(err: unknown): { title: string; detail?: string } {
  if (err instanceof ApiError) return err.hint ? { title: err.message, detail: err.hint } : { title: err.message };
  if (err instanceof DaemonUnreachableError) return { title: "Daemon unreachable", detail: err.message.replace(/^daemon-unreachable:\s*/, "") };
  if (typeof err === "string") return { title: err };
  if (err instanceof Error) return { title: err.message };
  return { title: "Something went wrong" };
}
