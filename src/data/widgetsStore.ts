import { apiFetch } from "../auth/api";
import type { Widget, WidgetAccent, WidgetConfig, WidgetStats, WidgetStatus } from "../types/widget";

/** Öffentliche, präsentationsbezogene Konfiguration (für widget.js / Standalone-Seite). */
export interface PublicWidgetConfig {
  id: string;
  status: string;
  knowledgeBaseId: string;
  routing: string;
  title: string;
  greeting: string;
  accentColor: string;
  position: string;
  icon: string;
  templates: string[];
  rules: string[];
  startPrompt: string;
  feedbackButtons: boolean;
  maxTokens?: number;
}

export function createDefaultConfig(): WidgetConfig {
  return {
    startPrompt: "Du bist ein hilfreicher Assistent. Beantworte Fragen freundlich und sachlich.",
    templates: [],
    rules: [],
    saveHistory: true,
    feedbackButtons: true,
    rateLimitPerMinute: 15,
    rateLimitPerUserPerDay: 75,
    maxTokensPerAnswer: 2000,
    title: "ChatBot",
    greeting: "Hallo! Wie kann ich dir helfen?",
    accentColor: "#0056b3",
    position: "bottom-right",
  };
}

/**
 * Raw widget as the backend returns it. The spec (`openapi.yaml`, schema
 * `Widget`) requires only `id`, `name`, `status` and `config`; `stats`, `accent`,
 * `icon`, `routing` and `knowledgeBaseId` may be missing, and the backend stores
 * the JSON verbatim (JSONB). Nothing beyond `id` may be assumed present.
 */
type RawWidget = { id: string } & Partial<Record<keyof Widget, unknown>> & { kbId?: unknown };

const WIDGET_STATUSES: readonly WidgetStatus[] = ["active", "paused"];
const WIDGET_ACCENTS: readonly WidgetAccent[] = ["primary", "secondary"];

function finiteOr(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function stringOr(value: unknown, fallback: string): string {
  return typeof value === "string" ? value : fallback;
}

function normalizeStats(raw: unknown): WidgetStats {
  const stats = raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};
  return {
    conversations: finiteOr(stats.conversations, 0),
    rating: finiteOr(stats.rating, 0),
  };
}

/**
 * Back-fills spec-optional (or malformed) fields so consumers such as
 * `WidgetCard` or the dashboard sort never dereference undefined (KI-823;
 * same idea as `normalizeAgent` in agentsStore.ts). Unlike there, unknown
 * fields are **kept** via the spread: the backend stores widgets verbatim and
 * `saveWidget` sends the whole object back, so dropping them would delete data
 * on the next save.
 *
 * Fallbacks: `status` → "paused" (the backend treats anything but "active" as
 * unavailable anyway, internal/widgets/chat.go), `accent` → "primary", `icon` →
 * "Bot" (the same fallback WidgetIcon/WidgetConfigView use), `stats` → 0/0.
 */
function normalizeWidget(raw: RawWidget): Widget {
  const status = WIDGET_STATUSES.find((s) => s === raw.status) ?? "paused";
  const accent = WIDGET_ACCENTS.find((a) => a === raw.accent) ?? "primary";
  const config =
    raw.config && typeof raw.config === "object" ? (raw.config as Partial<WidgetConfig>) : {};
  return {
    ...(raw as object),
    id: raw.id,
    name: stringOr(raw.name, ""),
    // Legacy data: the field used to be called `kbId`. If both are missing, ""
    // instead of undefined so consumers (e.g. ModelCombobox) never see undefined.
    knowledgeBaseId: stringOr(raw.knowledgeBaseId, stringOr(raw.kbId, "")),
    routing: stringOr(raw.routing, ""),
    status,
    icon: stringOr(raw.icon, "Bot"),
    accent,
    stats: normalizeStats(raw.stats),
    // Partial configs: fill missing fields from the defaults.
    config: { ...createDefaultConfig(), ...config },
  };
}

/**
 * Lädt alle Widgets vom Backend (GET /api/widgets). Quelle der Wahrheit ist das
 * Go-Backend (internal/widgets) – dieselben Daten, die auch das eingebettete
 * widget.js sieht.
 */
export async function fetchWidgets(): Promise<Widget[]> {
  const res = await apiFetch("/api/widgets");
  if (!res.ok) throw new Error(`Widgets konnten nicht geladen werden (HTTP ${res.status})`);
  const data = (await res.json()) as { widgets?: RawWidget[] };
  return (data.widgets ?? []).map(normalizeWidget);
}

/**
 * Lädt die öffentliche Konfiguration eines Widgets (für die Standalone-Seite /w/:id)
 * direkt vom Backend (GET /api/widgets/:id) – identisch zu dem, was widget.js abruft.
 */
export async function fetchPublicConfig(id: string): Promise<PublicWidgetConfig | null> {
  const res = await apiFetch(`/api/widgets/${encodeURIComponent(id)}`);
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(`Konfiguration konnte nicht geladen werden (HTTP ${res.status})`);
  return (await res.json()) as PublicWidgetConfig;
}

/** Legt ein Widget an oder aktualisiert es (PUT /api/widgets/:id). */
export async function saveWidget(widget: Widget): Promise<Widget> {
  const res = await apiFetch(`/api/widgets/${encodeURIComponent(widget.id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(widget),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error || `Speichern fehlgeschlagen (HTTP ${res.status})`);
  }
  return normalizeWidget((await res.json()) as RawWidget);
}

/**
 * Löscht ein Widget (DELETE /api/widgets/:id). Nur für Superadmins erlaubt –
 * das Backend gibt für andere Rollen 403 zurück.
 */
export async function deleteWidget(id: string): Promise<void> {
  const res = await apiFetch(`/api/widgets/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error || `Löschen fehlgeschlagen (HTTP ${res.status})`);
  }
}
