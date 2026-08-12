import { useEffect, useState } from "react";
import { ArrowUpDown, ListFilter, Search } from "lucide-react";
import { DashboardLayout, FilterMenu, Grid, Input, ListToolbar } from "@ki4jlu/design-system";
import { AddTile } from "../components/AddTile";
import { Alert } from "../components/Alert";
import { EmptyState } from "../components/EmptyState";
import { WidgetCard } from "../components/WidgetCard";
import { fetchAgents } from "../data/agentsStore";
import { fetchWidgets } from "../data/widgetsStore";
import type { Widget, WidgetStatus } from "../types/widget";

type StatusFilter = "all" | WidgetStatus;
type SortOption = "name" | "conversations" | "rating";

const FILTER_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "Alle" },
  { value: "active", label: "Aktiv" },
  { value: "paused", label: "Pause" },
];

const SORT_OPTIONS: { value: SortOption; label: string }[] = [
  { value: "name", label: "Name (A-Z)" },
  { value: "conversations", label: "Gespräche" },
  { value: "rating", label: "Bewertung" },
];

export function DashboardPage() {
  const [widgets, setWidgets] = useState<Widget[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  // Zuordnung agentId → Agent-Name, damit die Karten den verknüpften Agenten
  // statt einer Modell-ID zeigen. Fehlschläge sind unkritisch.
  const [agentNames, setAgentNames] = useState<Record<string, string>>({});

  useEffect(() => {
    fetchWidgets()
      .then((w) => {
        setWidgets(w);
        setLoadError(null);
      })
      .catch((err: unknown) => setLoadError(err instanceof Error ? err.message : "Unbekannter Fehler"));

    fetchAgents()
      .then((list) => {
        const map: Record<string, string> = {};
        for (const a of list) map[a.id] = a.name;
        setAgentNames(map);
      })
      .catch(() => setAgentNames({}));
  }, []);

  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [sortOption, setSortOption] = useState<SortOption>("name");

  const filteredWidgets = widgets
    .filter((widget) => {
      if (statusFilter !== "all" && widget.status !== statusFilter) return false;
      const query = search.trim().toLowerCase();
      if (!query) return true;
      return widget.name.toLowerCase().includes(query);
    })
    .sort((a, b) => {
      switch (sortOption) {
        case "conversations":
          return b.stats.conversations - a.stats.conversations;
        case "rating":
          return b.stats.rating - a.stats.rating;
        default:
          return a.name.localeCompare(b.name);
      }
    });

  return (
    <DashboardLayout
      title="Ihre Konnektoren"
      description={
        <>
          Konnektoren sind die <b className="text-on-surface">Front eines Agenten</b>: Chat, Formular oder
          API-Endpunkt.
        </>
      }
      actions={
        <span className="text-body-base text-on-surface-variant">
          {filteredWidgets.length} Konnektor{filteredWidgets.length === 1 ? "" : "en"}
        </span>
      }
      toolbar={
        <ListToolbar
          search={
            <Input
              leadingIcon={<Search aria-hidden />}
              placeholder="Konnektoren durchsuchen..."
              aria-label="Konnektoren durchsuchen"
              type="text"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          }
          filters={
            <>
              <FilterMenu
                icon={ListFilter}
                label="Filter"
                options={FILTER_OPTIONS}
                value={statusFilter}
                defaultValue="all"
                onChange={(value) => setStatusFilter(value as StatusFilter)}
              />
              <FilterMenu
                icon={ArrowUpDown}
                label="Sortieren"
                options={SORT_OPTIONS}
                value={sortOption}
                defaultValue="name"
                onChange={(value) => setSortOption(value as SortOption)}
              />
            </>
          }
        />
      }
    >
      {loadError && <Alert>Konnektoren konnten nicht geladen werden: {loadError}</Alert>}

      {!loadError && widgets.length > 0 && filteredWidgets.length === 0 && (
        <EmptyState title="Keine Konnektoren gefunden" hint="Suchbegriff oder Filter anpassen." />
      )}

      <Grid cols={4}>
        {filteredWidgets.map((widget) => (
          <WidgetCard
            key={widget.id}
            widget={widget}
            agentName={widget.agentId ? agentNames[widget.agentId] : undefined}
          />
        ))}
        <AddTile to="/widgets/new" label="Konnektor hinzufügen" hint="Deployment of an Agent" />
      </Grid>
    </DashboardLayout>
  );
}
