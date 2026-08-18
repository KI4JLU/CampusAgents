# Cross-Platform User Access (CampusAgents ↔ JustRAG) — Option A Summary

Two implementation plans have been written and are designed to be built and reviewed together:

- **CampusAgents** → [TOKEN_EXCHANGE_PLAN_CAMPUSAGENTS.md](TOKEN_EXCHANGE_PLAN_CAMPUSAGENTS.md)
- **JustRAG** → [../../JustRAG/docs/TOKEN_EXCHANGE_PLAN_JUSTRAG.md](../../JustRAG/docs/TOKEN_EXCHANGE_PLAN_JUSTRAG.md)

## The one finding that shapes everything

Option A is **not** "just config." **JustRAG only accepts its own HS256 JWT** — no JWKS, no issuer/audience validation (`JustRAG/go-backend/internal/auth/jwt.go` `ParseToken`). A Keycloak RS256 token fails its signature check instantly. So JustRAG needs real code to verify Keycloak tokens. Both sides need work.

## How the two plans split the work

**CampusAgents** (the harder lift — it creates the delegated identity):

1. Stop discarding Keycloak tokens at `OIDCCallback`; persist them encrypted in Redis (mandatory first step — today the server has nothing to exchange at agent-time).
2. RFC 8693 exchange client → ask Keycloak for a `justrag`-audienced token.
3. JustRAG API client behind an interface (so A2 vs A1 is a config switch).
4. Wire into the agent/chat handlers.

**JustRAG** (additive, low-risk if you take the recommended path):

- **Path A2 (recommended):** one new `POST /api/auth/oidc/exchange` endpoint that verifies the Keycloak token via JWKS, maps `sub`→user, and mints a normal JustRAG JWT. Every existing data endpoint stays untouched and per-user scoping just works.
- **Path A1 (alternative):** validate Keycloak tokens in the auth middleware on every request — purer, but touches the hot path.

## Three decisions that are genuinely yours to make

1. **Refresh-token lifetime vs. agent-flow timing** — if agents run hours/days after login, you need `offline_access` or a long SSO session, or exchanges fail. (CampusAgents Phase 0.4)
2. **JustRAG provisioning policy** — should an exchange auto-create a JustRAG account for someone who never logged into JustRAG directly? Recommended: start **strict** (reject with 403). (JustRAG Phase 2.3)
3. **A2 vs A1 on the JustRAG side** — recommended: A2.

## The critical security control

Both plans converge on this: **JustRAG must enforce that the token's audience is `justrag`.** That audience check is the entire confused-deputy protection — without it, any realm client's token works against JustRAG.
