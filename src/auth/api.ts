/**
 * Backend API client. All authenticated calls go through `apiFetch`, which
 * attaches the backend-issued JWT as a Bearer token and signals a global
 * "auth:unauthorized" event on a 401 so the AuthProvider can log the user out.
 *
 * The token is kept in a module-level variable (mirrored from React state by
 * AuthProvider) so non-React callers — e.g. the model-proxy helpers in
 * src/data — can attach it without threading the auth context through.
 */

/** Base URL of the backend. Empty string = same origin (Vite proxy in dev,
 *  nginx reverse-proxy in prod). */
export const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "";

const TOKEN_STORAGE_KEY = "campusagents.token";

let authToken: string | null = readStoredToken();

function readStoredToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_STORAGE_KEY);
  } catch {
    return null;
  }
}

/** Updates the in-memory + persisted token. Pass null to clear it. */
export function setAuthToken(token: string | null): void {
  authToken = token;
  try {
    if (token) localStorage.setItem(TOKEN_STORAGE_KEY, token);
    else localStorage.removeItem(TOKEN_STORAGE_KEY);
  } catch {
    /* storage unavailable (private mode) — in-memory token still works */
  }
}

/** Returns the current access token, or null if unauthenticated. */
export function getAuthToken(): string | null {
  return authToken;
}

/** Event dispatched when the backend rejects the token (401). */
export const UNAUTHORIZED_EVENT = "auth:unauthorized";

/**
 * fetch wrapper that prefixes API_BASE_URL, attaches the Bearer token, and
 * raises UNAUTHORIZED_EVENT on a 401 so the app can redirect to login.
 */
export async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers);
  if (authToken) headers.set("Authorization", `Bearer ${authToken}`);

  const response = await fetch(`${API_BASE_URL}${path}`, { ...init, headers });

  if (response.status === 401) {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT));
  }
  return response;
}
