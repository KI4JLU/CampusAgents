import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { apiFetch } from "../auth/api";
import type { components } from "../types/api.gen";
import { DashboardPage } from "./DashboardPage";

// Regression for KI-823: staging showed a white screen because WidgetCard read
// `widget.stats.rating` on a stored widget that has no `stats` (spec-optional).
// The page is driven through the real widgetsStore/agentsStore; only apiFetch
// (the network boundary) is mocked, so the normalization under test is the one
// the app actually uses.
vi.mock("../auth/api", () => ({ apiFetch: vi.fn() }));
const mockApiFetch = vi.mocked(apiFetch);

function jsonResponse(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as Response;
}

// Oracle: exactly the spec's `required: [id, name, status, config]` for schema
// `Widget` in openapi.yaml, typed against the spec-generated api.gen.ts, so
// `tsc -b` rejects this fixture if it is not spec-valid. It carries no `stats`,
// `accent`, `icon` or `routing` — the shape that crashed staging.
const SPEC_MINIMAL_WIDGET: components["schemas"]["Widget"] = {
  id: "minimal-1",
  name: "Konnektor ohne Stats",
  status: "active",
  config: {},
};

beforeEach(() => {
  mockApiFetch.mockReset();
  mockApiFetch.mockImplementation(async (path: string) => {
    if (path === "/api/widgets") return jsonResponse({ widgets: [SPEC_MINIMAL_WIDGET] });
    if (path === "/api/agents") return jsonResponse({ agents: [] });
    throw new Error(`unexpected request in test: ${path}`);
  });
});

describe("DashboardPage", () => {
  it("renders a spec-minimal widget instead of crashing (KI-823)", async () => {
    render(
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>,
    );

    // Before the fix, this render threw "Cannot read properties of undefined
    // (reading 'rating')" and React unmounted the tree.
    expect(await screen.findByText("Konnektor ohne Stats")).toBeInTheDocument();
    // Fallback stats from the card's acceptance criteria: { conversations: 0, rating: 0 },
    // rendered by WidgetCard as "0,0 / 5" (German decimal comma).
    expect(screen.getByText("0,0 / 5")).toBeInTheDocument();
    expect(screen.getByText("Aktiv")).toBeInTheDocument();
  });
});
