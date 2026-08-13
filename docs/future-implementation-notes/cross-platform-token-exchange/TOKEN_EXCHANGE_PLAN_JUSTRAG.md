# Implementation Plan — JustRAG side (Option A: OAuth 2.0 Token Exchange, RFC 8693)

**Goal:** Allow the CampusAgents service to call JustRAG's API *on behalf of a user*, scoped to that user's data, by presenting a Keycloak access token (obtained via RFC 8693 token exchange) that is audienced to JustRAG.

**Context:** CampusAgents and JustRAG federate the **same Keycloak realm** (`login.uni-giessen.de/realms/jlu`) and both already store the Keycloak `sub` in `users.external_id`. That shared key is what makes per-user scoping correct. See [`TOKEN_EXCHANGE_PLAN_CAMPUSAGENTS.md`](TOKEN_EXCHANGE_PLAN_CAMPUSAGENTS.md) for the matching client-side work.

---

## The core problem this plan solves

JustRAG's API authentication today accepts **only its own HS256 JWT**, signed with the local `JWTSecret`:

- [`internal/auth/jwt.go`](../go-backend/internal/auth/jwt.go) `ParseToken` rejects any non-HMAC signing method and verifies against the local secret. **No JWKS, no `iss` check, no `aud` check.**
- [`internal/auth/middleware.go`](../go-backend/internal/auth/middleware.go) `Authenticate` runs `ParseToken` then a `jti`/user-invalidation blacklist check.
- A parallel path in [`internal/apikeyauth/middleware.go`](../go-backend/internal/apikeyauth/middleware.go) accepts bcrypt-hashed `jrag_…` API keys and injects the **same** `auth.Claims` shape.

A Keycloak access token is **RS256**, carries `iss`/`aud`/`azp`, and would fail JustRAG's signature check immediately. So JustRAG must gain the ability to **verify a Keycloak access token and map it to a JustRAG user**.

---

## Two paths — pick one

### Path A2 — bootstrap exchange endpoint *(recommended)*

Add **one** new endpoint that trades a verified Keycloak access token for a normal JustRAG JWT. CampusAgents caches that JWT and uses JustRAG's **existing, unchanged** data endpoints.

- **Pro:** minimal change. The hot path (`auth.Middleware`) and every data handler stay exactly as they are. You reuse the existing JWT minting (`signToken` in [`internal/authhandler/handler.go`](../go-backend/internal/authhandler/handler.go)) and the existing blacklist/revocation machinery.
- **Con:** a second short-lived token (the JustRAG JWT) exists alongside the Keycloak token; CampusAgents re-bootstraps when it expires.

### Path A1 — validate Keycloak tokens directly in middleware

Teach `auth.Middleware` to accept Keycloak RS256 access tokens on every endpoint (detect token type, verify via JWKS, check `iss`/`aud`, map `sub`→user).

- **Pro:** no second token; token lifetime/revocation governed entirely by Keycloak.
- **Con:** touches the auth hot path for *all* requests; must coexist with the existing HS256 path and the API-key path; higher blast radius.

**Recommendation: implement A2.** It isolates all new, Keycloak-specific verification into a single endpoint and leaves the proven request path untouched. Everything below details A2, with A1 deltas noted at the end.

---

## Phase 0 — Keycloak realm prerequisites (shared with CampusAgents team)

Joint realm-admin tasks (also listed in the CampusAgents plan — do once):

1. Confirm/define the **`justrag` Keycloak client** and note its client id — this is the **audience** JustRAG will require.
2. Enable **token exchange** for the `chatwidgets` client with a policy permitting exchange **to the `justrag` audience**.
3. Ensure exchanged tokens carry `aud: justrag` (audience mapper on `chatwidgets`).
4. Note the realm **issuer URL** and **JWKS URI** (`<issuer>/protocol/openid-connect/certs`) — JustRAG needs these to verify tokens.

---

## Phase 1 — Keycloak access-token verifier

**File:** new `internal/auth/oidcverify.go`

1. Add a verifier that validates a **Keycloak access token**:
   - Fetch and **cache JWKS** from `<issuer>/protocol/openid-connect/certs` (use `github.com/coreos/go-oidc/v3` — already a dependency via the OIDC login flow in [`internal/authhandler/oidc.go`](../go-backend/internal/authhandler/oidc.go) — or `keyfunc` for raw JWKS). Cache with periodic refresh + refetch-on-unknown-kid.
   - Verify RS256 signature.
   - **Enforce `iss` == configured issuer.**
   - **Enforce `aud` (or `azp`) contains the JustRAG client id.** This is the confused-deputy protection — it's what stops a token minted for any *other* client from being replayed against JustRAG. Do not skip it.
   - Enforce `exp`/`nbf`.
2. Extract claims: `sub`, `preferred_username`, `email`, `given_name`, `family_name` (same shape as the existing `oidcClaims` struct in `oidc.go` ~line 601 — reuse it).

**Config (new, in [`internal/config/config.go`](../go-backend/internal/config/config.go)):**

| Var | Purpose |
|-----|---------|
| `KEYCLOAK_ISSUER_URL` | expected `iss` (may equal the existing OIDC provider issuer) |
| `KEYCLOAK_JWKS_URL` | JWKS endpoint (or derive from issuer discovery) |
| `KEYCLOAK_EXPECTED_AUDIENCE` | the `justrag` client id required in `aud`/`azp` |

If JustRAG's existing single OIDC provider already points at the same realm, reuse its issuer/JWKS rather than adding parallel config — just add the **expected-audience** check.

---

## Phase 2 — Exchange endpoint

**File:** new handler in `internal/authhandler/` (e.g. `exchange.go`), route in [`internal/app/routes.go`](../go-backend/internal/app/routes.go) near the existing `/api/auth/oidc/*` routes (~line 364).

`POST /api/auth/oidc/exchange`

1. Read the bearer token from `Authorization`. **Do not** run the normal `auth.Middleware` on this route (it would reject the Keycloak token). This route is unauthenticated at the JustRAG-JWT layer and self-authenticates via the verifier.
2. Verify it with the Phase 1 verifier. On failure → `401`.
3. Map to a user via `sub`:
   - Reuse `resolveOIDCUser` ([`oidc.go`](../go-backend/internal/authhandler/oidc.go) ~line 666): look up by `external_id == sub`; fall back to `preferred_username` linking; else provision.
   - **Provisioning decision (REQUIRED):** Should an exchange auto-create a JustRAG user who has never logged into JustRAG directly? Two options:
     - **Strict (recommended to start):** reject with `403 no_justrag_account` if no `external_id` match. Users must have an existing JustRAG account. Safer; avoids surprise account creation from a sibling service.
     - **Permissive:** auto-provision like a first OIDC login. If you choose this, **explicitly disable the "first OIDC user becomes superadmin" heuristic for this path** — an exchange-provisioned user must default to role `user`. Audit `resolveOIDCUser`/role assignment before enabling.
4. Mint a JustRAG JWT for the resolved user using the existing `signToken(userID, username, role)` — identical to the OIDC login path, so all downstream endpoints and the blacklist work unchanged.
5. Return `{ "token": "<JustRAG JWT>", "user": {...} }`, mirroring the existing login response shape.

**Hardening:**
- Rate-limit this endpoint per `sub`.
- Log `auth.exchange_validated` / `auth.exchange_rejected` with `sub` (not the token) for audit.
- Consider restricting `azp` to the known `chatwidgets` client id as defense-in-depth (only CampusAgents should be bootstrapping sessions this way).

---

## Phase 3 — User mapping & data scoping (verify, mostly no-op)

No data-layer changes needed for A2 — once the exchange mints a standard JustRAG JWT, the existing per-user scoping applies automatically. Confirm:

- Data endpoints scope by `user.ID` from context — verified in [`internal/kb/store_pg.go`](../go-backend/internal/kb/store_pg.go) `ListKnowledgeBases` (filters `kb.user_id = $1` + shares), chats by `chats.user_id`, etc.
- The `external_id` unique index exists (`migrations/main/0047_user_external_id.sql`) so the `sub` lookup is correct and fast.

**Action:** add a test asserting that a token for user A's `sub` yields a JWT whose `id` is user A, and that the resulting calls cannot see user B's KBs.

---

## Phase 4 — Testing & rollout

1. **Unit:** JWKS verifier (valid/expired/wrong-issuer/wrong-audience/unknown-kid → correct accept/reject); `sub`→user mapping incl. the strict no-account rejection.
2. **Integration (staging realm):** real exchanged Keycloak token from CampusAgents → `/api/auth/oidc/exchange` → JustRAG JWT → data call returns the right user's data.
3. **Security tests (must pass before rollout):**
   - Token audienced to a *different* client → `401` (confused-deputy guard).
   - Expired / tampered token → `401`.
   - Valid token for a user with no JustRAG account → `403` (strict mode).
   - User isolation: A's token never returns B's resources.
4. **Rollout:** feature-flag the endpoint (`OIDC_EXCHANGE_ENABLED`). Deploy disabled, enable in staging, validate with CampusAgents, then production. The endpoint is purely additive — existing auth paths are untouched, so rollback is just disabling the flag.

---

## Path A1 deltas (only if you choose direct validation instead of A2)

- Modify [`internal/auth/middleware.go`](../go-backend/internal/auth/middleware.go) `Authenticate` to branch on token type: try the Phase-1 Keycloak verifier first (RS256 / has `kid` / `iss` matches), else fall back to the existing HS256 `ParseToken`. The API-key middleware (`jrag_` prefix) is unaffected.
- On a Keycloak token, build `auth.Claims` by mapping `sub`→user (same `resolveOIDCUser` logic) **on every request** — so add a short per-`sub` user-lookup cache to avoid a DB hit per call.
- Blacklist/`jti` semantics differ (Keycloak controls revocation); decide whether the existing blacklist applies to externally-issued tokens or is bypassed for them.
- No exchange endpoint and no second token; CampusAgents sends the exchanged Keycloak token directly to data endpoints.
- Higher risk: every request now flows through new verification code. Prefer A2 unless you specifically want Keycloak to own token lifetime end to end.

---

## Risks / decisions to confirm

- **Provisioning policy** (Phase 2.3) — strict vs. permissive. Start strict.
- **Superadmin heuristic** — if you ever allow auto-provisioning here, the "first OIDC user → superadmin" rule must not fire for exchange-provisioned users.
- **Audience enforcement** (Phase 1) is the single most important security control. A verifier that skips `aud`/`azp` turns JustRAG into an open resource server for any realm client.
- **JWKS availability** — JustRAG now has a runtime dependency on Keycloak being reachable for verification (A1: every request; A2: only at exchange). Cache JWKS and fail closed.
