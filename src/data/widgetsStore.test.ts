import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiFetch } from "../auth/api";
import {
  createDefaultConfig,
  fetchPublicConfig,
  fetchWidgets,
  saveWidget,
} from "./widgetsStore";
import type { Widget } from "../types/widget";
import type { components } from "../types/api.gen";

// Der Store spricht das Backend ausschließlich über apiFetch an – hier gemockt,
// damit die Tests kein echtes Netzwerk/Backend brauchen.
vi.mock("../auth/api", () => ({ apiFetch: vi.fn() }));
const mockApiFetch = vi.mocked(apiFetch);

// Baut eine minimale Response, wie sie apiFetch zurückgibt (nur die von den
// Store-Funktionen genutzten Felder: ok, status, json()).
function jsonResponse(body: unknown, init: { status?: number } = {}): Response {
  const status = init.status ?? 200;
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

function makeWidget(overrides: Partial<Widget> = {}): Widget {
  return {
    id: "w1",
    name: "Test Widget",
    knowledgeBaseId: "kb-1",
    routing: "public",
    status: "active",
    icon: "Globe",
    accent: "primary",
    stats: { conversations: 0, rating: 0 },
    config: createDefaultConfig(),
    ...overrides,
  };
}

beforeEach(() => {
  mockApiFetch.mockReset();
});

describe("createDefaultConfig", () => {
  it("liefert die aktuellen Defaults ohne die entfernten Felder", () => {
    const config = createDefaultConfig();
    expect(config.maxTokensPerAnswer).toBe(2000);
    expect(config.saveHistory).toBe(true);
    expect(config.feedbackButtons).toBe(true);
    // apiKey/model/dialogDepth wurden aus WidgetConfig entfernt.
    expect("apiKey" in config).toBe(false);
    expect("model" in config).toBe(false);
  });
});

describe("fetchWidgets", () => {
  it("ruft GET /api/widgets ab und liefert die Widget-Liste", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: [makeWidget({ id: "a" })] }));

    const widgets = await fetchWidgets();
    expect(mockApiFetch).toHaveBeenCalledWith("/api/widgets");
    expect(widgets).toHaveLength(1);
    expect(widgets[0].id).toBe("a");
  });

  it("übernimmt das alte Feld kbId in knowledgeBaseId", async () => {
    const legacy = { ...makeWidget(), knowledgeBaseId: undefined, kbId: "kb-legacy" };
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: [legacy] }));

    const widgets = await fetchWidgets();
    expect(widgets[0].knowledgeBaseId).toBe("kb-legacy");
  });

  it("setzt knowledgeBaseId auf \"\" statt undefined, wenn beides fehlt", async () => {
    const legacy = { ...makeWidget(), knowledgeBaseId: undefined };
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: [legacy] }));

    const widgets = await fetchWidgets();
    expect(widgets[0].knowledgeBaseId).toBe("");
  });

  it("ergänzt fehlende config-Felder mit den Defaults", async () => {
    const partial = { ...makeWidget(), config: { title: "Nur Titel" } };
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: [partial] }));

    const widgets = await fetchWidgets();
    expect(widgets[0].config.title).toBe("Nur Titel");
    // Nicht gesendete Felder kommen aus createDefaultConfig().
    expect(widgets[0].config.maxTokensPerAnswer).toBe(2000);
    expect(widgets[0].config.saveHistory).toBe(true);
  });

  it("liefert ein leeres Array, wenn das Backend keine widgets-Property sendet", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse({}));
    await expect(fetchWidgets()).resolves.toEqual([]);
  });

  it("wirft bei einer Fehlerantwort des Backends", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ error: "kaputt" }, { status: 500 }));
    await expect(fetchWidgets()).rejects.toThrow(/HTTP 500/);
  });
});

// Spec-minimal widget: exactly the fields of `required: [id, name, status, config]`
// of schema `Widget` in openapi.yaml, nothing else.
// Oracle: the type comes from src/types/api.gen.ts, which openapi-typescript
// generates from the spec, not from the code under test. `tsc -b` (npm run build)
// checks that this object is spec-valid; if the spec ever adds a required field,
// the typecheck breaks here. The expected fallback values are taken from the
// acceptance criteria of card KI-823, not from a run of the code.
const SPEC_MINIMAL_WIDGET: components["schemas"]["Widget"] = {
  id: "minimal-1",
  name: "Nur Pflichtfelder",
  status: "active",
  config: {},
};

describe("fetchWidgets with spec-minimal / malformed widgets (KI-823)", () => {
  it("back-fills every optional presentation field of a spec-minimal widget", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: [SPEC_MINIMAL_WIDGET] }));

    const [widget] = await fetchWidgets();
    // Required fields pass through unchanged.
    expect(widget.id).toBe("minimal-1");
    expect(widget.name).toBe("Nur Pflichtfelder");
    expect(widget.status).toBe("active");
    // Optional fields get the fallbacks fixed in the card.
    expect(widget.stats).toEqual({ conversations: 0, rating: 0 });
    expect(widget.accent).toBe("primary");
    expect(widget.icon).toBe("Bot");
    expect(widget.routing).toBe("");
    expect(widget.knowledgeBaseId).toBe("");
    expect(widget.config.maxTokensPerAnswer).toBe(2000);
  });

  it("replaces malformed stats with 0, field by field", async () => {
    const payload = [
      { ...SPEC_MINIMAL_WIDGET, id: "null-stats", stats: null },
      { ...SPEC_MINIMAL_WIDGET, id: "string-rating", stats: { conversations: 7, rating: "4.5" } },
      { ...SPEC_MINIMAL_WIDGET, id: "nan", stats: { conversations: Number.NaN, rating: 3 } },
    ];
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: payload }));

    const widgets = await fetchWidgets();
    expect(widgets.map((w) => w.stats)).toEqual([
      { conversations: 0, rating: 0 },
      { conversations: 7, rating: 0 },
      { conversations: 0, rating: 3 },
    ]);
  });

  it("maps an unknown accent to primary and an unknown status to paused", async () => {
    const payload = [{ ...SPEC_MINIMAL_WIDGET, accent: "tertiary", status: "archived", icon: 42 }];
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: payload }));

    const [widget] = await fetchWidgets();
    expect(widget.accent).toBe("primary");
    // The backend (chat.go) treats anything but "active" as unavailable.
    expect(widget.status).toBe("paused");
    expect(widget.icon).toBe("Bot");
  });

  it("keeps valid values untouched and preserves unknown fields", async () => {
    const full = { ...makeWidget({ accent: "secondary", status: "paused", icon: "Globe" }), extra: "bleibt" };
    full.stats = { conversations: 12, rating: 4.25 };
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ widgets: [full] }));

    const [widget] = await fetchWidgets();
    expect(widget.accent).toBe("secondary");
    expect(widget.status).toBe("paused");
    expect(widget.icon).toBe("Globe");
    expect(widget.stats).toEqual({ conversations: 12, rating: 4.25 });
    // The backend stores verbatim; saveWidget sends the whole object back.
    expect((widget as Widget & { extra?: string }).extra).toBe("bleibt");
  });
});

describe("saveWidget", () => {
  it("sendet PUT an /api/widgets/:id mit dem Widget als Body", async () => {
    const widget = makeWidget({ id: "new-1", name: "Neu" });
    mockApiFetch.mockResolvedValueOnce(jsonResponse(widget));

    await saveWidget(widget);

    expect(mockApiFetch).toHaveBeenCalledTimes(1);
    const [path, init] = mockApiFetch.mock.calls[0];
    expect(path).toBe("/api/widgets/new-1");
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toMatchObject({ id: "new-1", name: "Neu" });
  });

  it("kodiert die id in der URL", async () => {
    const widget = makeWidget({ id: "a/b" });
    mockApiFetch.mockResolvedValueOnce(jsonResponse(widget));

    await saveWidget(widget);
    expect(mockApiFetch.mock.calls[0][0]).toBe("/api/widgets/a%2Fb");
  });

  it("gibt das vom Backend gespeicherte Widget zurück", async () => {
    const saved = makeWidget({ id: "w1", name: "Serverwert" });
    mockApiFetch.mockResolvedValueOnce(jsonResponse(saved));

    const result = await saveWidget(makeWidget({ id: "w1", name: "Clientwert" }));
    expect(result.name).toBe("Serverwert");
  });

  it("also normalizes the backend response (spec-minimal to complete)", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse(SPEC_MINIMAL_WIDGET));

    const result = await saveWidget(makeWidget({ id: "minimal-1" }));
    expect(result.stats).toEqual({ conversations: 0, rating: 0 });
    expect(result.accent).toBe("primary");
  });

  it("wirft mit der Backend-Fehlermeldung bei einer Fehlerantwort", async () => {
    mockApiFetch.mockResolvedValueOnce(
      jsonResponse({ error: "Widget-id im Body muss zur URL passen." }, { status: 400 }),
    );
    await expect(saveWidget(makeWidget())).rejects.toThrow("Widget-id im Body muss zur URL passen.");
  });
});

describe("fetchPublicConfig", () => {
  it("liefert die öffentliche Konfiguration eines vorhandenen Widgets", async () => {
    mockApiFetch.mockResolvedValueOnce(
      jsonResponse({ id: "pub-1", knowledgeBaseId: "kb-9", title: "Titel" }),
    );

    const config = await fetchPublicConfig("pub-1");
    expect(mockApiFetch).toHaveBeenCalledWith("/api/widgets/pub-1");
    expect(config?.knowledgeBaseId).toBe("kb-9");
    expect(config?.title).toBe("Titel");
  });

  it("liefert null für ein unbekanntes Widget (404)", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ error: "nicht gefunden" }, { status: 404 }));
    await expect(fetchPublicConfig("gibt-es-nicht")).resolves.toBeNull();
  });

  it("wirft bei anderen Fehlerantworten", async () => {
    mockApiFetch.mockResolvedValueOnce(jsonResponse({ error: "kaputt" }, { status: 500 }));
    await expect(fetchPublicConfig("x")).rejects.toThrow(/HTTP 500/);
  });
});
