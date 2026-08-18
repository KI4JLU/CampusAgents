import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  API_BASE_URL,
  apiFetch,
  getAuthToken,
  setAuthToken,
  UNAUTHORIZED_EVENT,
} from "./api";

/** User identity returned by the backend on login / in the callback fragment. */
export interface AuthUser {
  id: string;
  username: string;
  role: string;
  authMethod?: string;
}

interface AuthContextValue {
  user: AuthUser | null;
  isAuthenticated: boolean;
  /** Local username/password login. Throws on failure (caller shows the error). */
  loginLocal: (username: string, password: string) => Promise<void>;
  /** Redirect to the backend OIDC broker (Keycloak). */
  loginWithSSO: () => void;
  /** Store a session obtained from the OIDC callback fragment. */
  setSession: (token: string, user: AuthUser) => void;
  /** Clear the session (and, for OIDC users, hit the IdP RP-logout). */
  logout: () => void;
}

const USER_STORAGE_KEY = "campusagents.user";

function readStoredUser(): AuthUser | null {
  try {
    const raw = localStorage.getItem(USER_STORAGE_KEY);
    return raw ? (JSON.parse(raw) as AuthUser) : null;
  } catch {
    return null;
  }
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  // Seed from storage so a reload keeps the session. The token lives in api.ts.
  const [user, setUser] = useState<AuthUser | null>(() =>
    getAuthToken() ? readStoredUser() : null,
  );

  const persist = useCallback((token: string | null, nextUser: AuthUser | null) => {
    setAuthToken(token);
    setUser(nextUser);
    try {
      if (nextUser) localStorage.setItem(USER_STORAGE_KEY, JSON.stringify(nextUser));
      else localStorage.removeItem(USER_STORAGE_KEY);
    } catch {
      /* storage unavailable */
    }
  }, []);

  const setSession = useCallback(
    (token: string, nextUser: AuthUser) => persist(token, nextUser),
    [persist],
  );

  const loginLocal = useCallback(
    async (username: string, password: string) => {
      const res = await apiFetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(body?.error ?? "Anmeldung fehlgeschlagen");
      }
      const data = (await res.json()) as { token: string; user: AuthUser };
      persist(data.token, data.user);
    },
    [persist],
  );

  const loginWithSSO = useCallback(() => {
    window.location.href = `${API_BASE_URL}/api/auth/oidc/login`;
  }, []);

  const logout = useCallback(() => {
    const wasOidc = user?.authMethod === "oidc";
    const token = getAuthToken();
    persist(null, null);

    if (wasOidc) {
      // RP-initiated logout: the backend blacklists the JWT (if still passed)
      // and redirects to Keycloak's end_session_endpoint.
      window.location.href = `${API_BASE_URL}/api/auth/oidc/logout`;
      return;
    }
    // Local accounts: best-effort server-side blacklist, then go to /login.
    if (token) {
      void fetch(`${API_BASE_URL}/api/auth/logout`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
      }).catch(() => {});
    }
    window.location.assign("/login");
  }, [persist, user]);

  // A 401 from any apiFetch means the token is dead — drop the session.
  useEffect(() => {
    const handler = () => persist(null, null);
    window.addEventListener(UNAUTHORIZED_EVENT, handler);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, handler);
  }, [persist]);

  const value = useMemo<AuthContextValue>(
    () => ({
      user,
      isAuthenticated: user !== null && getAuthToken() !== null,
      loginLocal,
      loginWithSSO,
      setSession,
      logout,
    }),
    [user, loginLocal, loginWithSSO, setSession, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/** Access the auth context. Must be used within <AuthProvider>. */
// Provider + hook live together by design; this disables the fast-refresh-only
// lint that prefers them in separate files.
// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within an AuthProvider");
  return ctx;
}
