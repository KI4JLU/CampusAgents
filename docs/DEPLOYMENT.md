# Chatbot Admin — Deployment Guide

One **frontend** (SPA + widget loader, served by Nginx) and one **Go backend**
(auth, API keys, model proxy) with **Postgres** + **Redis**. The SPA calls
same-origin `/api`; Nginx reverse-proxies `/api` to the backend. All secrets live
in the backend — nothing sensitive is bundled into the SPA. Auth details:
[AUTHENTICATION.md](./AUTHENTICATION.md).

| Component | Port | Role |
| --- | --- | --- |
| frontend (Nginx) | 80 / 443 | static SPA + `/widget.js`; proxies `/api` → backend |
| backend (Go) | 8080 | JWT/OIDC auth, API keys, model proxy |
| Postgres / Redis | — | users, providers, API keys / JWT revocation |
| widget mock-portal | 6443 (server) / 8082 (local) | cross-origin site that embeds the widget |

> **Mock widget portal** — available on **every** deployment as a **separate,
> cross-origin origin**: locally the `widget-test-site` container on `http://…:8082`;
> on a TLS server the **frontend nginx serves it over HTTPS on `:6443`** (same
> cert, a different port = a different origin). Override either with
> `VITE_WIDGET_PORTAL_URL`. It embeds the widget and logs in against the admin's
> real backend (`POST /api/auth/login`), so it always exercises the true
> cross-origin flow — which means the portal's origin must be in the backend's
> `ALLOWED_ORIGINS`. It's linked from the admin user menu (admins only). Browsing
> to `http://<host>:8080` returns **404** — the backend serves only `/api/*` and
> `/healthz`.

There are exactly three deployments:

---

## 1. Local Development

Live code reload (Vite HMR) **and** the cross-origin widget portal, one command:

```bash
npm install        # first time only
npm run dev
```

`npm run dev` starts the backend (Docker: Postgres + Redis + migrate + serve),
the widget portal, and Vite. No `.env` needed — a **`admin` / `password`**
superadmin is seeded.

- **Admin UI (live reload):** http://localhost:5173 → log in `admin` / `password`.
- **Widget mock-portal (cross-origin):** http://localhost:8082 → log in
  `admin` / `password`, then **Widget neu laden**. Its *Widget Server Origin* is
  pre-filled to `http://localhost:5173` (the dev admin), so the portal on `:8082`
  talks to the admin on `:5173` — a genuine cross-origin test.

Stop with `Ctrl-C` (Vite) then `npm run backend:down`. To customise the admin
password, `KI_API_KEY`, or OIDC, copy `go-backend/.env.example` →
`go-backend/.env`, edit, and `npm run backend:up`.

| Script | Action |
| --- | --- |
| `npm run dev` | Backend + widget portal + Vite. |
| `npm run dev:frontend` | Vite only. |
| `npm run backend:up` / `backend:logs` / `backend:down` | Manage the backend. |

---

## 2. Staging

A real server (`sv90073.hrz.uni-giessen.de`) running ready-made images. The compose
project lives in **`/root/widgets`**, so every command below needs root — the
deploy account (`gz488`) is deliberately *not* in the `docker` group and uses
passwordless sudo instead:

```bash
ssh gz488@sv90073.hrz.uni-giessen.de
sudo sh -c 'cd /root/widgets && docker compose ps'
```

On the server, in that directory:

```bash
cp .env.staging.example .env     # fill in every FILL_IN value (secrets + OIDC)
docker compose pull              # frontend + backend images from GHCR
docker compose up -d             # frontend + backend + Postgres + Redis + portal
```

**Service names are asymmetric** — check before typing a per-service command, or
compose fails with "no such service":

| Service (compose) | Container | Image |
| --- | --- | --- |
| `campusagents-frontend` | `campusagents-frontend` | `ghcr.io/ki4jlu/campusagents-frontend` |
| `backend` | `campusagents-backend` | `ghcr.io/ki4jlu/campusagents-backend` |
| `postgres` / `redis` / `migrate` | `widgets-<name>-1` | official images |

`docker compose config --services` is the authoritative list.

- **No build runs on the server.** Both the **frontend**
  (`ghcr.io/ki4jlu/campusagents-frontend`) and the **backend**
  (`ghcr.io/ki4jlu/campusagents-backend`) are prebuilt images published by
  [`.github/workflows/docker-publish.yml`](../.github/workflows/docker-publish.yml)
  on every push to `main`. `pull_policy: always` keeps them fresh.
  > The owner segment follows `github.repository_owner`, so it is **`ki4jlu`**
  > since the repo moved to the KI4JLU org. The old `stenseegel/*` images are
  > stale — pulling those is why a deploy can silently keep serving an old
  > build. New packages are **private by default**: after the first push under a
  > new image name, set each package to public (Org → Packages → Package
  > settings), or give the server a `docker login ghcr.io` with a token
  > carrying `read:packages`.

**Deploying only the frontend** — the right move for a `widget.js`, SPA or nginx
change, since `public/widget.js` ships inside the frontend image and nothing else
is affected:

```bash
sudo sh -c 'cd /root/widgets && docker compose pull campusagents-frontend'
sudo sh -c 'cd /root/widgets && docker compose up -d --no-deps campusagents-frontend'
```

`--no-deps` is the point: without it, compose evaluates `depends_on` and may
recreate Postgres, Redis and the backend alongside the frontend. Those carry the
state, so keeping them on their existing uptime turns a widget deploy into a
genuinely narrow operation — and it sidesteps the Postgres-role hazard below
entirely. Verify from *outside* the server, since a cached copy on the way in
would otherwise look like a failed deploy:

```bash
curl -sk https://sv90073.hrz.uni-giessen.de/widget.js | grep -c scriptBase   # ≥1 = new loader
curl -sk -o /dev/null -w '%{http_code}\n' https://sv90073.hrz.uni-giessen.de/api/widgets/support-bot
```

The loader is served `max-age=300, must-revalidate`, so allow up to five minutes
before concluding a deploy didn't land.

> **Umbenennung `chatbotadmin-*` → `campusagents-*`.** Betrifft auch
> `POSTGRES_USER`/`POSTGRES_DB`. Postgres legt Rolle und Datenbank **nur beim
> ersten Start auf einem leeren Volume** an — auf einem bestehenden Volume
> ändert die Umbenennung nichts, und der Backend-Start scheitert dann an einer
> Rolle, die es dort nicht gibt. Beim Update deshalb das Volume verwerfen:
>
> ```bash
> docker compose down -v      # löscht pgdata dieses Projekts
> docker compose pull
> docker compose up -d        # legt Rolle/DB neu an, migrate seedet
> ```
>
> Das ist **Datenverlust** — auf Staging bewusst in Kauf genommen (Testdaten).
> Auf einer Instanz mit echten Daten stattdessen Rolle und DB in Postgres
> umbenennen, statt das Volume zu löschen.
- **TLS** is served by the frontend on 80/443 using the host certs
  (`/etc/ssl/certs/sv90073.pem`, `/etc/ssl/private/priv.pem`) and
  `nginx.staging.conf` — already wired in `docker-compose.yml`. (If `:443` is
  taken, map `"442:443"` and add `:442` to the URLs + OIDC redirect URIs.)
- **Admin UI:** https://sv90073.hrz.uni-giessen.de — once OIDC is on, the **first
  SSO login becomes superadmin**, so log in immediately (see AUTHENTICATION.md).
- **Widget test portal:** the frontend nginx serves it over TLS on **`:6443`**
  (cross-origin from the admin, same cert — see `nginx.staging.conf`). Its origin
  (`https://sv90073.hrz.uni-giessen.de:6443`) is in `ALLOWED_ORIGINS` so the
  cross-origin login works. The plain-HTTP `widget-test-site` container is
  local-only (compose `local` profile) and is **not** started on the server.

**Required `.env`** (full template in [`.env.staging.example`](../.env.staging.example)):

| Var | Notes |
| --- | --- |
| `GO_ENV=production` | Fail-closed token revocation; makes `ALLOWED_ORIGINS` required. |
| `POSTGRES_PASSWORD`, `JWT_SECRET` (≥32), `AUTH_PROVIDER_SECRET_KEY` (base64 32 B) | Core secrets. |
| `ALLOWED_ORIGINS` | The admin origin, e.g. `https://sv90073.hrz.uni-giessen.de`, **plus every external site that embeds the widget** (see note below). CORS is backend-driven by this. |
| `ADMIN_PASSWORD`, `KI_API_KEY` | Seed admin (fallback) + HRZ model proxy. |
| `OIDC_*` | Keycloak (JLU `jlu` realm); redirect URIs use the staging host. See AUTHENTICATION.md. |
| `BACKEND_HTTP_PROXY`, `BACKEND_HTTPS_PROXY`, `BACKEND_NO_PROXY` | Only if the host reaches the internet via the HRZ proxy. |

---

## 3. Production

Identical to staging, but runs **only the prod-ready image** instead of `latest`.
Promote a validated staging image, then deploy with that tag pinned:

```bash
# Promote the tested images (CI or manually) — tag both frontend and backend:
for img in campusagents-frontend campusagents-backend; do
  docker tag  ghcr.io/ki4jlu/$img:latest ghcr.io/ki4jlu/$img:prod
  docker push ghcr.io/ki4jlu/$img:prod
done

# On the prod server — .env pins the prod tags plus prod secrets/origins:
docker compose pull
docker compose up -d
```

The only differences from staging are in the prod `.env`: `FRONTEND_IMAGE_TAG=prod`
and `BACKEND_IMAGE_TAG=prod` (so it runs the promoted images, not `latest`), the
production domain in `ALLOWED_ORIGINS`, and the production `OIDC_*` redirect URIs.

---

## Embedding the Widget

Applies to any deployment (staging or production) — replace the origin below
with that deployment's own domain:

```html
<div class="chatbot-widget" data-widget-id="support-bot" data-kb="jlu-staging-2026" data-lang="de"></div>
<script src="https://sv90073.hrz.uni-giessen.de/widget.js" defer></script>
```

> **Embedding on a real external site (not the mock portal)** — `widget.js`
> fetches its live config from `GET /api/widgets/{id}` cross-origin, which the
> backend's CORS middleware (`go-backend/internal/middleware/cors.go`) only
> allows for **exact-match origins in `ALLOWED_ORIGINS`** (scheme + host +
> port, no wildcards). The external site's origin must be added there:
> ```
> ALLOWED_ORIGINS=https://sv90073.hrz.uni-giessen.de,https://sv90073.hrz.uni-giessen.de:6443,<external site origin>
> ```
> then `docker compose up -d backend` to pick up the change (only the backend
> reads this var; no rebuild needed).
>
> **Why this fails silently:** if the origin isn't allowlisted, the browser
> blocks the config fetch as a CORS violation, and `widget.js` swallows that
> in a `catch` and falls back to its hardcoded generic defaults (title
> "ChatBot Support", generic greeting/templates) — see `public/widget.js`
> around the `fetch(`${apiBase}/widgets/...`)` call. The widget still *loads*
> and looks functional, it just silently doesn't reflect the real, DB-configured
> widget. There's no console error to point at this — if an embedded widget
> looks "generic" or out of date on one site but correct on another, check
> `ALLOWED_ORIGINS` before anything else.

### Embedding behind a reverse proxy on the portal (Plone)

Loading the loader straight from `sv90073.hrz.uni-giessen.de` makes the portal
serve a script from a foreign host, which trips the portal's security review. The
fix on the portal side is a reverse proxy, so the loader is same-origin. **The
proxy must forward the API too, under the same path prefix as the loader** —
`widget.js` resolves every backend call *relative to its own `<script src>`*
(`new URL('api', <dir of src>)`, see the `scriptBase` comment in
`public/widget.js`), so loader and API always travel together:

| `widget.js` served as | Widget calls |
|---|---|
| `https://portal/widget.js` | `https://portal/api/widgets/{id}` |
| `https://portal/campusagents/widget.js` | `https://portal/campusagents/api/widgets/{id}` |

Use a **dedicated prefix**, not the portal root. Mapping the loader to
`https://portal/widget.js` sends the API calls to `https://portal/api/…`, which on
`www.uni-giessen.de` is Plone's own namespace — it answers
`404 {"error_type": "NotFound"}`, so the widget silently renders its hardcoded
fallback config and every question fails with `⚠️ HTTP 404`.

Name the prefix after **this deployment**, not after the widget type: it is a
permanent public URL, and `/campusagents/` still fits once non-chat widgets ship.
Avoid functional names (`/chatbot/`, `/embed/`, `/widgets/`) — they age badly, and
a Plone editor could create content at that path, which a `^~` location then
silently shadows. Nginx on the portal:

```nginx
# One prefix, forwarded to the CampusAgents deployment. Serves BOTH
# /campusagents/widget.js and /campusagents/api/… — the loader needs both under
# the same prefix. The trailing slash on proxy_pass strips the prefix.
location ^~ /campusagents/ {
    proxy_pass https://sv90073.hrz.uni-giessen.de/;

    proxy_ssl_server_name on;                              # nginx defaults this OFF
    proxy_ssl_name        sv90073.hrz.uni-giessen.de;

    proxy_set_header Host              sv90073.hrz.uni-giessen.de;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_http_version 1.1;
    proxy_read_timeout 300s;   # chat streaming (SSE)
    proxy_buffering    off;    # stream tokens promptly, don't buffer the answer
}
```

Three details in that block are load-bearing:

- **`^~`**, not a bare prefix and not `=`. `^~` wins over regex locations, which a
  Plone vhost normally has for static assets (`location ~* \.(js|css)$`) — without
  it, `/campusagents/widget.js` gets served by Plone's static handler instead of
  proxied. `=` is an exact match and can only ever forward one file, which is why
  a loader-only `location = /widget.js` cannot be extended to cover the API.
- **Trailing slash on both** `location …/` and `proxy_pass …de/`. That pair strips
  the prefix upstream (`/campusagents/api/widgets/x` → `/api/widgets/x`). Naming a
  file in `proxy_pass` (`…de/widget.js`) pins the block to that one file.
- **`proxy_buffering off` + `proxy_read_timeout 300s`.** Irrelevant for a static
  file, essential here: chat answers are an SSE stream, so buffering makes the
  visitor watch the typing dots until the model finishes, and the default 60s read
  timeout truncates long answers mid-stream.

Verify after `nginx -t && nginx -s reload` — both must return 200, the second is
the one a loader-only proxy gets wrong:

```bash
curl -si https://www.uni-giessen.de/campusagents/widget.js                    | head -1
curl -si https://www.uni-giessen.de/campusagents/api/widgets/support-bot      | head -1
```

The embed snippet then carries no foreign host at all:

```html
<div class="chatbot-widget" data-widget-id="support-bot"></div>
<script src="/campusagents/widget.js" defer></script>
```

`proxy_buffering off` and the raised `proxy_read_timeout` are not optional: chat
answers are an SSE stream, and a buffering proxy holds the whole answer back until
the model finishes (or times out at the default 60s on a long answer).

Because everything is now same-origin from the browser's point of view, this
layout needs **no `ALLOWED_ORIGINS` entry** for the portal and sends **no
cross-origin preflight** — the CORS caveat above simply doesn't apply.

**Fallback if the portal will only proxy the loader** and not the API: pin the API
explicitly on the placeholder, which overrides the relative resolution.

```html
<div class="chatbot-widget" data-widget-id="support-bot"
     data-api="https://sv90073.hrz.uni-giessen.de/api"></div>
<script src="/widget.js" defer></script>
```

This reintroduces the cross-origin path, so the portal's origin must be in
`ALLOWED_ORIGINS`. It keeps the script same-origin (which is what the security
review was about) but the `fetch` calls still leave the domain — prefer the
prefix proxy above.
