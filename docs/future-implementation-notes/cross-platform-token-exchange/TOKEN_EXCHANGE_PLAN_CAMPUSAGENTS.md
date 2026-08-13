# Implementation Plan — CampusAgents side (Option A: OAuth 2.0 Token Exchange, RFC 8693)

**Goal:** Let CampusAgents's agent flows call the JustRAG backend API *on behalf of the logged-in user*, scoped to that user's JustRAG data, without sharing a database and without per-user secrets.

**Mechanism:** CampusAgents holds the user's Keycloak session, performs an RFC 8693 token exchange at Keycloak to obtain a token *audienced to JustRAG*, and uses it to authenticate to JustRAG. See `TOKEN_EXCHANGE_PLAN_JUSTRAG.md` for the matching server-side work.

> This plan pairs with the JustRAG plan. Neither delivers value alone — coordinate the Keycloak realm changes (Phase 0) jointly.

---

## The blocker this plan removes

Today, after the OIDC code exchange in [`oidc.go`](../go-backend/internal/authhandler/oidc.go) (`OIDCCallback`, ~line 313), CampusAgents verifies the **ID token**, mints its **own HS256 JWT**, and **discards the Keycloak access and refresh tokens**. Auth is fully stateless — at agent-execution time the server only has its own `Claims{id, username, role}`, nothing it can present to Keycloak or JustRAG.

So the first and mandatory change is: **stop discarding the Keycloak tokens; persist them server-side** so a later agent flow can exchange them.

---

## End-to-end flow (target state)

```
Login (once):
  Browser → Keycloak → CampusAgents /api/auth/oidc/callback
    └─ verify id_token, mint CampusAgents JWT (unchanged)
    └─ NEW: persist {access_token, refresh_token, expiry} in Redis, keyed by user id

Agent flow (later, server-side):
  agent handler has auth.Claims{ID,...} in request context
    └─ load user's Keycloak tokens from Redis (refresh if access_token expired)
    └─ RFC 8693 token-exchange call to Keycloak token endpoint:
         subject_token = user's fresh Keycloak access_token
         audience      = JustRAG's Keycloak client id
       → receive JustRAG-audienced access_token (still represents the user, same sub)
    └─ bootstrap JustRAG session: POST that token to JustRAG /api/auth/oidc/exchange
       → receive a JustRAG JWT (cache it in Redis, short TTL)
    └─ call JustRAG data endpoints with the JustRAG JWT
```

The "bootstrap to a JustRAG JWT" hop exists because JustRAG's data endpoints accept only JustRAG's own JWT (see JustRAG plan, recommended path A2). If JustRAG instead chooses to validate Keycloak tokens directly on every endpoint (path A1), CampusAgents skips the bootstrap hop and sends the exchanged Keycloak token straight to the data endpoints. **Build CampusAgents's JustRAG client behind an interface so this is a one-line switch.**

---

## Phase 0 — Keycloak realm prerequisites (shared with JustRAG team)

These are realm-admin tasks on `login.uni-giessen.de/realms/jlu`, done jointly:

1. **Confirm a JustRAG client exists** (e.g. `justrag`) and note its client id — this is the `audience` for the exchange.
2. **Enable token exchange** for the `chatwidgets` client (CampusAgents's confidential client). In modern Keycloak this is the *standard token exchange v2* capability on the client, plus a policy that permits `chatwidgets` to exchange **to the `justrag` audience**. Without this policy Keycloak returns `403 / not_allowed`.
3. **Add `justrag` to `chatwidgets`'s allowed audiences** (audience mapper / client scope) so the exchanged token carries `aud: justrag`.
4. **Refresh-token lifetime:** agent flows can happen long after login. Either ensure the SSO Session Idle/Max covers the expected gap, or request the `offline_access` scope at login so CampusAgents gets an offline refresh token. **Decision required** — pick based on how long after login agent flows realistically run. If you add `offline_access`, add it to `OIDC_SCOPES`.

Document the final client ids and the exchange policy in `docs/AUTHENTICATION.md`.

---

## Phase 1 — Persist Keycloak tokens at login

**File:** new `internal/authhandler/oidc.go` changes + new `internal/kctokens/store.go`

1. New Redis-backed store `kctokens.Store` (reuse the existing `redisclient.Client` wired in [`app.go`](../go-backend/internal/app/app.go) ~line 38; it already uses `github.com/redis/go-redis/v9`):
   - `Save(ctx, userID string, t KCTokens) error` — JSON-encode `{access_token, refresh_token, access_expiry, refresh_expiry}`; key `kc:tokens:<userID>`; TTL = refresh-token lifetime (or a fixed max, e.g. 30d, for offline tokens).
   - `Load(ctx, userID string) (KCTokens, bool, error)`.
   - `Delete(ctx, userID string) error` — for logout.
2. In `OIDCCallback`, **before** the tokens fall out of scope (currently ~line 313–327), capture `tok.AccessToken`, `tok.RefreshToken`, and `tok.Expiry`, and call `kctokens.Save(...)` keyed by the resolved JustRAG user id (the same `user.ID` that goes into the minted JWT).
3. **Encrypt at rest.** These are live user credentials. Reuse the existing AES helper already used for `OIDC_CLIENT_SECRET` (`AUTH_PROVIDER_SECRET_KEY`, see [`config.go`](../go-backend/internal/config/config.go) ~line 198) to encrypt the token blob before `Set`, decrypt after `Get`. Do **not** store raw tokens in Redis.
4. **Clear on logout.** In the logout handler, call `kctokens.Delete(userID)` so a revoked session can't be exchanged. (Also relevant: CampusAgents's existing JWT blacklist already handles its own token; this adds the Keycloak side.)

**Security note:** access tokens are short-lived; the refresh token is the sensitive long-lived secret. Encryption + TTL + delete-on-logout are mandatory, not optional.

---

## Phase 2 — Token-exchange client

**File:** new `internal/kcexchange/client.go`

A small client that calls Keycloak's token endpoint. Follow the outbound-HTTP pattern from [`modelproxy/handler.go`](../go-backend/internal/modelproxy/handler.go) (`http.NewRequestWithContext`, a shared `*http.Client`, `defer resp.Body.Close()`).

1. **Ensure a fresh access token.** Given a `userID`:
   - `Load` tokens from `kctokens.Store`. If `access_expiry` is within ~30s, do a `grant_type=refresh_token` call to mint a fresh access token, and `Save` the rotated tokens back (Keycloak rotates refresh tokens by default).
   - If the refresh token is itself expired/invalid → return a typed `ErrReauthRequired` so callers can surface "please re-login to access JustRAG data".
2. **Exchange.** POST to `<issuer>/protocol/openid-connect/token` with:
   ```
   grant_type=urn:ietf:params:oauth:grant-type:token-exchange
   subject_token=<fresh user access_token>
   subject_token_type=urn:ietf:params:oauth:token-type:access_token
   audience=<JustRAG client id>            # or requested_token_type per Keycloak version
   ```
   Client auth = `chatwidgets` id + secret via HTTP Basic (`Authorization: Basic base64(id:secret)`) or form fields. Both are available from the OIDC config already loaded in `config.go` (`ClientID`, decrypted `ClientSecret`, derived issuer).
3. Return the exchanged `access_token` (+ its expiry).
4. **Config:** add `JUSTRAG_AUDIENCE` (the `justrag` Keycloak client id) and `KEYCLOAK_TOKEN_ENDPOINT` (or derive it from the existing OIDC provider discovery, which `oidc.go` already fetches via `provider.Endpoint()`).

Keep exchange results cacheable: short Redis cache keyed `kc:exchanged:<userID>` with TTL just under the token's `expires_in`, to avoid hitting Keycloak on every agent step.

---

## Phase 3 — JustRAG API client

**File:** new `internal/justrag/client.go`

1. Define an interface so the auth strategy is swappable:
   ```go
   type Client interface {
       // returns an Authorization header value usable against JustRAG data endpoints
       AuthForUser(ctx context.Context, userID string) (bearer string, err error)
       ListKnowledgeBases(ctx context.Context, userID string) ([]KB, error)
       // ...the specific data calls agent flows need
   }
   ```
2. **Default impl (matches JustRAG path A2 — bootstrap to JustRAG JWT):**
   - `AuthForUser`: check Redis cache `jr:jwt:<userID>`. If miss/expired → get an exchanged Keycloak token (Phase 2) → `POST <JUSTRAG_BASE_URL>/api/auth/oidc/exchange` with `Authorization: Bearer <exchanged token>` → receive `{token}` (JustRAG JWT) → cache with TTL just under the JWT's exp.
   - Data calls use that JustRAG JWT as `Authorization: Bearer <jwt>`.
3. **Alternative impl (matches JustRAG path A1 — direct token validation):** `AuthForUser` just returns the exchanged Keycloak token; no bootstrap call. Selected by config flag `JUSTRAG_AUTH_MODE=exchange|direct`.
4. **Config:** `JUSTRAG_BASE_URL` (internal network URL of the JustRAG backend, e.g. `http://justrag-backend:8080`). Confirm network reachability — today CampusAgents only talks to its own backend and the HRZ model endpoint; JustRAG must be on a reachable network/route.

---

## Phase 4 — Wire into agent flows

1. Identify the agent/chat handlers that need JustRAG data (the model-proxy handlers at `GET /api/models`, `POST /api/chat` in [`router.go`](../go-backend/internal/app/router.go), and any new agent-orchestration handler).
2. In those handlers, pull `claims := auth.UserFromContext(r.Context())` (already populated by the auth middleware) and use `claims.ID` as the `userID` for `justrag.Client.AuthForUser` and data calls.
3. **Failure modes to handle explicitly:**
   - `ErrReauthRequired` (refresh token dead) → return a structured error the frontend can turn into a "reconnect JustRAG" prompt; do **not** 500.
   - JustRAG 401/403 → the user exists in Keycloak but has no/insufficient JustRAG access. Surface as "no JustRAG access", not a crash.
   - JustRAG unreachable → degrade the agent flow gracefully.

---

## Phase 5 — Config & docs summary

New env vars (add to `go-backend/.env.example` and compose):

| Var | Purpose |
|-----|---------|
| `JUSTRAG_BASE_URL` | JustRAG backend base URL (internal network) |
| `JUSTRAG_AUDIENCE` | JustRAG's Keycloak client id (exchange `audience`) |
| `JUSTRAG_AUTH_MODE` | `exchange` (A2, default) or `direct` (A1) |
| `OIDC_SCOPES` | add `offline_access` *if* Phase 0 chose offline tokens |

Update `docs/AUTHENTICATION.md` with the new flow diagram and the Keycloak exchange policy.

---

## Phase 6 — Testing & rollout

1. **Unit:** `kcexchange` refresh+exchange logic (mock Keycloak token endpoint); `justrag.Client` bootstrap+cache; encryption round-trip in `kctokens`.
2. **Integration (staging realm):** real Keycloak token exchange end to end; assert the exchanged token's `aud` contains `justrag` and `sub` is unchanged.
3. **E2E:** log into CampusAgents via SSO → trigger an agent flow → confirm it reads the *correct* user's JustRAG KBs, and that user B cannot see user A's data.
4. **Negative:** expired refresh token → `ErrReauthRequired`; logout clears `kc:tokens:<user>`; user with no JustRAG account → clean error.
5. **Rollout:** ship behind a feature flag (`JUSTRAG_INTEGRATION_ENABLED`). The token-persistence change (Phase 1) is safe to deploy first on its own; exchange + agent wiring follow once JustRAG's side is live.

---

## Risks / decisions to confirm

- **Refresh-token lifetime vs. agent-flow latency** (Phase 0.4) — the one most likely to bite. If agent flows run hours/days after login and you didn't request `offline_access`, exchanges will fail with dead refresh tokens.
- **Storing refresh tokens server-side** raises the blast radius of a CampusAgents Redis compromise. Encryption-at-rest + TTL + logout-delete mitigate; document the tradeoff.
- **Confused-deputy protection lives on the JustRAG side** (audience check). CampusAgents must request the correct `audience`; JustRAG must enforce it. Coordinate.
