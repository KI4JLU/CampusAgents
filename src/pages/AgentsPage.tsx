import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Badge, Button, Card, DashboardLayout, FilterMenu, Grid, Input, ListToolbar } from "@ki4jlu/design-system";
import { ArrowUpDown, Bot, Brain, Link as LinkIcon, ListFilter, Pencil, Search } from "lucide-react";
import { AddTile } from "../components/AddTile";
import { Alert } from "../components/Alert";
import { EmptyState } from "../components/EmptyState";
import { agentUsageByWidgets, fetchAgents } from "../data/agentsStore";
import { fetchModels } from "../data/models";
import { fetchWidgets } from "../data/widgetsStore";
import type { Agent } from "../types/agent";

type ConnectorFilter = "all" | "used" | "unused";
type SortOption = "name" | "connectors" | "rules";

const FILTER_OPTIONS: { value: ConnectorFilter; label: string }[] = [
  { value: "all", label: "Alle" },
  { value: "used", label: "Mit Konnektoren" },
  { value: "unused", label: "Standalone" },
];

const SORT_OPTIONS: { value: SortOption; label: string }[] = [
  { value: "name", label: "Name (A-Z)" },
  { value: "connectors", label: "Konnektoren" },
  { value: "rules", label: "Regeln" },
];

/** Zählt die aktiven, nicht-leeren Regeln eines Agenten (Karten-Stat + Sortierung). */
function activeRuleCount(agent: Agent): number {
  return agent.rules.filter((r) => r.enabled && r.text.trim()).length;
}

export function AgentsPage() {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [usage, setUsage] = useState<Record<string, number>>({});
  const [modelNames, setModelNames] = useState<Record<string, string>>({});
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    fetchAgents()
      .then((a) => {
        setAgents(a);
        setLoadError(null);
      })
      .catch((err: unknown) => setLoadError(err instanceof Error ? err.message : "Unbekannter Fehler"));

    // Nutzung je Agent aus den Konnektoren (Widgets) ableiten.
    fetchWidgets()
      .then((widgets) => setUsage(agentUsageByWidgets(widgets)))
      .catch(() => setUsage({}));

    // Modell-IDs auf Klarnamen auflösen (Fehlschlag unkritisch → dann greift die ID).
    fetchModels()
      .then((models) => {
        const map: Record<string, string> = {};
        for (const m of models) if (m.name) map[m.id] = m.name;
        setModelNames(map);
      })
      .catch(() => setModelNames({}));
  }, []);

  const [search, setSearch] = useState("");
  const [connectorFilter, setConnectorFilter] = useState<ConnectorFilter>("all");
  const [sortOption, setSortOption] = useState<SortOption>("name");

  const filteredAgents = agents
    .filter((agent) => {
      const used = usage[agent.id] ?? 0;
      if (connectorFilter === "used" && used === 0) return false;
      if (connectorFilter === "unused" && used > 0) return false;
      const query = search.trim().toLowerCase();
      if (!query) return true;
      return agent.name.toLowerCase().includes(query);
    })
    .sort((a, b) => {
      switch (sortOption) {
        case "connectors":
          return (usage[b.id] ?? 0) - (usage[a.id] ?? 0);
        case "rules":
          return activeRuleCount(b) - activeRuleCount(a);
        default:
          return a.name.localeCompare(b.name);
      }
    });

  return (
    <DashboardLayout
      title="Agenten"
      description={
        <>
          Agenten sind die wiederverwendbare <b className="text-on-surface">Denkschicht</b>: Modell, System-Prompt
          und Regeln.
        </>
      }
      actions={
        <span className="text-body-base text-on-surface-variant">
          {filteredAgents.length} Agent{filteredAgents.length === 1 ? "" : "en"}
        </span>
      }
      toolbar={
        <ListToolbar
          search={
            <Input
              leadingIcon={<Search aria-hidden />}
              placeholder="Agenten durchsuchen..."
              aria-label="Agenten durchsuchen"
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
                value={connectorFilter}
                defaultValue="all"
                onChange={(value) => setConnectorFilter(value as ConnectorFilter)}
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
      {loadError && <Alert>Agenten konnten nicht geladen werden: {loadError}</Alert>}

      {!loadError && agents.length > 0 && filteredAgents.length === 0 && (
        <EmptyState title="Keine Agenten gefunden" hint="Suchbegriff oder Filter anpassen." />
      )}

      <Grid cols={4}>
        {filteredAgents.map((agent) => {
          const used = usage[agent.id] ?? 0;
          const activeRules = activeRuleCount(agent);
          return (
            <Card key={agent.id} interactive className="@container p-4 flex flex-col">
              <div className="flex justify-between items-start mb-3">
                <div className="w-10 h-10 bg-primary/10 rounded-xl flex items-center justify-center">
                  <Brain className="text-primary" width="1em" height="1em" aria-hidden />
                </div>
                {used > 0 ? (
                  <Badge tone="primary">
                    <LinkIcon className="text-[14px]" width="1em" height="1em" aria-hidden />
                    {used} Konnektor{used === 1 ? "" : "en"}
                  </Badge>
                ) : (
                  <Badge tone="neutral">Standalone</Badge>
                )}
              </div>

              <div className="mb-3">
                <h4 className="font-headline-md text-base font-bold truncate">{agent.name || "(ohne Namen)"}</h4>
                <div className="flex items-center gap-2 text-on-surface-variant mt-1">
                  <Bot className="text-sm" width="1em" height="1em" aria-hidden />
                  <span className="font-label-sm text-xs truncate">{modelNames[agent.model] || agent.model || "kein Modell"}</span>
                </div>
              </div>

              <div className="border-t border-outline-variant/30 my-3" />

              {/* Container-Query statt Viewport-Breakpoint: die Kartenbreite
                  hängt an der Spaltenzahl des Rasters, nicht am Viewport. */}
              <div className="grid grid-cols-1 gap-2 mb-4 @[14rem]:grid-cols-2">
                <div className="flex flex-col min-w-0">
                  <span className="text-xs text-on-surface-variant truncate">Regeln</span>
                  <span className="font-semibold text-sm truncate">{activeRules} aktiv</span>
                </div>
                <div className="flex flex-col min-w-0 @[14rem]:border-l @[14rem]:border-outline-variant/30 @[14rem]:pl-2">
                  <span className="text-xs text-on-surface-variant truncate">Max. Tokens</span>
                  <span className="font-semibold text-sm truncate">{agent.maxTokens}</span>
                </div>
              </div>

              <Button asChild variant="outline" size="sm" className="mt-auto w-full min-w-0">
                <Link to={`/agents/${agent.id}`}>
                  <Pencil className="text-[16px]" width="1em" height="1em" aria-hidden />
                  <span className="truncate">Bearbeiten</span>
                </Link>
              </Button>
            </Card>
          );
        })}

        <AddTile to="/agents/new" label="Agent hinzufügen" hint="Prompt, Model, Tools, Knowledge" />
      </Grid>
    </DashboardLayout>
  );
}
