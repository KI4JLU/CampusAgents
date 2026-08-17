/**
 * Integration + rendering tests for the embedded widget (public/widget.js).
 *
 * These load the REAL widget.js IIFE into the jsdom document, mock fetch for the
 * two calls it makes — GET /api/widgets/:id (config) and POST /api/widgets/:id/chat
 * (the SSE answer stream) — and then drive the DOM the way a user would:
 *
 *   Nutzerfrage im Widget → widget.js ruft die API → (gemockte) KB antwortet
 *   mit Quellen → das Widget rendert die Antwort.
 *
 * widget.js is plain browser JS (not an ES module), so we read it from disk and
 * evaluate it via new Function() after the DOM + fetch mock are in place.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

const WID = "pe-bot";
// vitest runs with the project root as cwd; widget.js is plain browser JS.
const widgetSource = readFileSync(resolve(process.cwd(), "public/widget.js"), "utf8");

// jsdom does not implement HTMLElement.innerText. widget.js uses it to set
// plain-text bubbles (user messages, error messages), so without this shim those
// nodes stay empty in the test DOM. Browsers map innerText to the rendered text;
// mapping it to textContent is a faithful-enough stand-in for assertions.
Object.defineProperty(HTMLElement.prototype, "innerText", {
  configurable: true,
  get(): string {
    return this.textContent ?? "";
  },
  set(value: string) {
    this.textContent = value;
  },
});

// Public config the widget fetches on init (shape of GET /api/widgets/:id).
const publicConfig = {
  id: WID,
  status: "active",
  knowledgeBaseId: "kb-1ca5e21b-9b38-4be2-b242-073d50e3c3bb",
  routing: "internal",
  title: "PE Bot",
  greeting: "Hallo! Wie kann ich helfen?",
  accentColor: "#0056b3",
  position: "bottom-right",
  icon: "smart_toy",
  templates: [],
  rules: [],
  startPrompt: "Du bist ein Test-Assistent.",
  feedbackButtons: true,
  maxTokens: 2000,
};

// Frame one SSE event exactly like the backend does: a JSON payload on a
// "data:" line, terminated by a blank line.
function sseEvent(obj: unknown): string {
  return `data: ${JSON.stringify(obj)}\n\n`;
}

// A ReadableStream of encoded SSE events — what res.body.getReader() consumes.
function sseStream(events: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const e of events) controller.enqueue(enc.encode(e));
      controller.close();
    },
  });
}

// Bodies posted to the chat endpoint, captured for contract assertions.
let chatBodies: Array<Record<string, unknown>>;

// Install a fetch mock: chat POST returns the given SSE events (or an error
// response), the config GET returns publicConfig, everything else 404s.
function mockFetch(chatEvents: string[], chatError?: { status: number; error: string }) {
  globalThis.fetch = vi.fn(async (url: unknown, opts?: { body?: string }) => {
    const u = String(url);
    if (u.includes(`/api/widgets/${WID}/chat`)) {
      chatBodies.push(JSON.parse(opts?.body ?? "{}"));
      if (chatError) {
        return { ok: false, status: chatError.status, json: async () => ({ error: chatError.error }) } as unknown as Response;
      }
      return { ok: true, status: 200, body: sseStream(chatEvents) } as unknown as Response;
    }
    if (u.includes(`/api/widgets/${WID}`)) {
      return { ok: true, status: 200, json: async () => publicConfig } as unknown as Response;
    }
    return { ok: false, status: 404, json: async () => ({}) } as unknown as Response;
  }) as typeof fetch;
}

// Evaluate widget.js; its IIFE scans the DOM and initialises our placeholder.
function runWidget() {
  new Function(widgetSource)();
}

// Minimal real-timer poll (avoids coupling to testing-library / fake timers).
async function waitFor(assertion: () => void, timeout = 3000): Promise<void> {
  const start = Date.now();
  for (;;) {
    try {
      assertion();
      return;
    } catch (err) {
      if (Date.now() - start > timeout) throw err;
      await new Promise((r) => setTimeout(r, 10));
    }
  }
}

// widget.js mounts its UI inside a shadow root (for CSS encapsulation), on a
// host div appended as the only child of the `.chatbot-widget` placeholder.
// document.querySelector can't pierce that boundary, so tests must go through
// the shadow root explicitly.
function widgetShadowRoot(): ShadowRoot | null {
  const container = document.querySelector(".chatbot-widget");
  const host = container?.firstElementChild as (Element & { shadowRoot: ShadowRoot | null }) | null;
  return host?.shadowRoot ?? null;
}

function $(sel: string): HTMLElement | null {
  return (widgetShadowRoot()?.querySelector(sel) as HTMLElement | null) ?? null;
}

// Open the widget and submit a question through its real input + send button.
async function openAndSend(text: string) {
  await waitFor(() => expect($(".chatbot-fab")).toBeTruthy());
  ($(".chatbot-fab") as HTMLElement).click();
  const input = $(".chatbot-input") as HTMLInputElement;
  expect(input).toBeTruthy();
  input.value = text;
  ($(".chatbot-send") as HTMLElement).click();
}

beforeEach(() => {
  document.body.innerHTML = `<div class="chatbot-widget" data-widget-id="${WID}"></div>`;
  chatBodies = [];
});

describe("widget.js — Konfiguration & KB-Integration", () => {
  it("lädt die öffentliche Konfiguration und rendert den Titel", async () => {
    mockFetch([]);
    runWidget();

    await waitFor(() => expect($(".chatbot-widget-wrapper")).toBeTruthy());
    expect($(".chatbot-widget-wrapper")!.textContent).toContain("PE Bot");
  });

  it("schickt die Frage an den per-Widget-Chat-Endpunkt — nur messages + stream, keine Modell-Angabe", async () => {
    mockFetch([sseEvent({ content: "Antwort" }), sseEvent({ done: true, finishReason: "stop" })]);
    runWidget();

    await openAndSend("Was ist das PE Programm?");
    await waitFor(() => expect(chatBodies.length).toBe(1));

    const body = chatBodies[0];
    expect(body.stream).toBe(true);
    expect((body.messages as Array<{ content: string }>).some((m) => m.content.includes("PE Programm"))).toBe(true);
    // Der Client darf Modell/KB/Token-Limit NICHT vorgeben — das löst der Server auf.
    expect(body).not.toHaveProperty("model");
    expect(body).not.toHaveProperty("knowledgeBaseId");
    expect(body).not.toHaveProperty("maxTokens");

    // Der Request ging an den öffentlichen per-Widget-Endpunkt.
    const calls = (globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    const chatUrl = calls.map((c) => String(c[0])).find((u) => u.includes("/chat"));
    expect(chatUrl).toContain(`/api/widgets/${WID}/chat`);
  });
});

describe("widget.js — Auflösung der API-URL", () => {
  // widget.js darf keinen absoluten Host enthalten: es löst die API relativ zu
  // seiner EIGENEN <script src> auf (Verzeichnis, nicht nur Origin). Nur so
  // funktioniert ein Reverse Proxy auf der einbettenden Seite, der einen Prefix
  // auf dieses Deployment mappt — sonst landen die Requests im Wurzelpfad des
  // Portals, wo die Anwendung des Portals antwortet und nicht unsere.
  function withCurrentScript(src: string, run: () => void) {
    Object.defineProperty(document, "currentScript", {
      configurable: true,
      get: () => ({ src }) as unknown as HTMLScriptElement,
    });
    try {
      run();
    } finally {
      // @ts-expect-error -- Instanz-Override entfernen, damit der Prototyp-Getter wieder greift.
      delete document.currentScript;
    }
  }

  // Die Konfigurations-URL ist der Aufruf, der NICHT auf "/chat" endet — auf
  // `includes("/chat")` zu prüfen wäre falsch, weil ein Proxy-Prefix diese
  // Zeichenfolge selbst enthalten kann (z. B. "/chatbot/").
  function configUrl(): string | undefined {
    const calls = (globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    return calls.map((c) => String(c[0])).find((u) => !u.endsWith("/chat"));
  }

  it("hängt die API unter denselben Pfad-Prefix, unter dem widget.js ausgeliefert wurde", async () => {
    mockFetch([]);
    withCurrentScript("https://www.uni-giessen.de/campusagents/widget.js", runWidget);

    await waitFor(() => expect(configUrl()).toBeTruthy());
    expect(configUrl()).toBe(`https://www.uni-giessen.de/campusagents/api/widgets/${WID}`);
  });

  // Der Prefix ist im Loader nicht bekannt: die einbettende Seite wählt ihn und
  // darf ihn ändern, ohne dass hier etwas angepasst werden muss.
  it("funktioniert unter einem beliebigen, mehrstufigen Prefix", async () => {
    mockFetch([]);
    withCurrentScript("https://portal.example/dienste/assistenten/widget.js", runWidget);

    await waitFor(() => expect(configUrl()).toBeTruthy());
    expect(configUrl()).toBe(`https://portal.example/dienste/assistenten/api/widgets/${WID}`);
  });

  it("ruft bei Auslieferung aus dem Wurzelverzeichnis /api auf demselben Host auf", async () => {
    mockFetch([]);
    withCurrentScript("https://sv90073.hrz.uni-giessen.de/widget.js", runWidget);

    await waitFor(() => expect(configUrl()).toBeTruthy());
    expect(configUrl()).toBe(`https://sv90073.hrz.uni-giessen.de/api/widgets/${WID}`);
  });

  it("data-api überschreibt die relative Auflösung", async () => {
    document.body.innerHTML =
      `<div class="chatbot-widget" data-widget-id="${WID}" data-api="https://sv90073.hrz.uni-giessen.de/api"></div>`;
    mockFetch([]);
    withCurrentScript("https://www.uni-giessen.de/widget.js", runWidget);

    await waitFor(() => expect(configUrl()).toBeTruthy());
    expect(configUrl()).toBe(`https://sv90073.hrz.uni-giessen.de/api/widgets/${WID}`);
  });

  it("lädt keine externen Ressourcen (kein Web-Font von einem Drittanbieter-Host)", async () => {
    mockFetch([]);
    withCurrentScript("https://www.uni-giessen.de/widget.js", runWidget);

    await waitFor(() => expect($(".chatbot-widget-wrapper")).toBeTruthy());
    const external = [...document.querySelectorAll("link, script, img")]
      .map((el) => el.getAttribute("href") ?? el.getAttribute("src") ?? "")
      .filter((u) => /^https?:\/\//.test(u));
    expect(external).toEqual([]);
  });
});

describe("widget.js — Antwort-Darstellung", () => {
  it("rendert die gestreamte KB-Antwort inklusive Quellen", async () => {
    mockFetch([
      sseEvent({ content: "Das PE Programm ist das Fortbildungsprogramm der JLU [1]." }),
      sseEvent({ content: "\n\n**Quellen:**\n[1] JLU-Fortbildungsprogramm_2026.pdf, p. 11-13" }),
      sseEvent({ done: true, finishReason: "stop" }),
    ]);
    runWidget();

    await openAndSend("Was ist das PE Programm?");

    const messages = () => $(".chatbot-messages")!;
    await waitFor(() => expect(messages().textContent).toContain("Quellen"));

    // Antworttext und Quellen sind sichtbar.
    expect(messages().textContent).toContain("Fortbildungsprogramm");
    expect(messages().textContent).toContain("JLU-Fortbildungsprogramm_2026.pdf");
    // Die "Quellen"-Überschrift wird als Markdown (fett) gerendert.
    const strongs = [...messages().querySelectorAll("strong")].map((s) => s.textContent ?? "");
    expect(strongs.some((s) => s.includes("Quellen"))).toBe(true);
  });

  it("zeigt eine Fehlermeldung, wenn der Chat-Endpunkt ablehnt", async () => {
    mockFetch([], { status: 403, error: "Dieses Widget ist derzeit nicht verfügbar." });
    runWidget();

    await openAndSend("Frage");

    const messages = () => $(".chatbot-messages")!;
    await waitFor(() => expect(messages().textContent).toContain("nicht verfügbar"));
    expect(messages().textContent).toContain("⚠️");
  });
});
