import { Link } from "react-router-dom";
import { Badge, Button, Card } from "@ki4jlu/design-system";
import { Brain, Code, MessageCircle, Settings, type LucideIcon } from "lucide-react";
import { WidgetIcon } from "./WidgetIcon";
import type { Widget } from "../types/widget";

const accentClasses: Record<Widget["accent"], { iconBg: string; iconText: string }> = {
  primary: { iconBg: "bg-primary/10", iconText: "text-primary" },
  secondary: { iconBg: "bg-secondary/10", iconText: "text-secondary" },
};

const statusBadge: Record<Widget["status"], { tone: "primary" | "neutral"; label: string }> = {
  active: { tone: "primary", label: "Aktiv" },
  paused: { tone: "neutral", label: "Pause" },
};

// Footer-Buttons (`path` wird an `/widgets/${id}` angehängt). „Gespräche" ist
// der EINZIGE Einstieg in /widgets/:id/gespraeche, seit die Sidebar-Subnavigation
// entfallen ist — nicht entfernen, ohne einen anderen Einstieg zu schaffen.
// Drei Buttons passen inzwischen: auf schmalen Karten stapeln sie (Container-Query).
const footerActions: { path: string; icon: LucideIcon; label: string }[] = [
  { path: "", icon: Settings, label: "Einstellungen" },
  { path: "/gespraeche", icon: MessageCircle, label: "Gespräche" },
  { path: "/einbetten", icon: Code, label: "Einbetten" },
];

interface WidgetCardProps {
  widget: Widget;
  /** Name des verknüpften Agenten (aufgelöst aus agentId). */
  agentName?: string;
}

export function WidgetCard({ widget, agentName }: WidgetCardProps) {
  const accent = accentClasses[widget.accent];
  const status = statusBadge[widget.status];
  const rating = widget.stats.rating.toFixed(1).replace(".", ",");

  return (
    <Card interactive className="@container p-4 flex flex-col">
      <div className="flex justify-between items-start mb-3">
        <div className={`w-10 h-10 ${accent.iconBg} rounded-xl flex items-center justify-center`}>
          <WidgetIcon name={widget.icon} className={accent.iconText} />
        </div>
        <Badge dot tone={status.tone}>
          {status.label}
        </Badge>
      </div>

      <div className="mb-3">
        <h4 className="font-headline-md text-base font-bold">{widget.name}</h4>
        <div className="flex items-center gap-2 text-on-surface-variant mt-1">
          <Brain className="text-sm" width="1em" height="1em" aria-hidden />
          <span className="font-label-sm text-xs truncate">{agentName || "kein Agent"}</span>
        </div>
      </div>

      <div className="border-t border-outline-variant/30 my-3" />

      {/* Im 4er-Raster wird die Karte zu schmal für drei Spalten bzw. zwei
          Buttons — Container-Query statt Viewport-Breakpoint, weil die
          Kartenbreite an der Spaltenzahl hängt, nicht am Viewport. */}
      <div className="grid grid-cols-1 gap-2 mb-4 @[16rem]:grid-cols-3">
        <div className="flex flex-col min-w-0">
          <span className="text-xs text-on-surface-variant truncate">Routing</span>
          <span className="font-semibold text-sm truncate">{widget.routing}</span>
        </div>
        <div className="flex flex-col min-w-0 @[16rem]:border-l @[16rem]:border-outline-variant/30 @[16rem]:pl-2">
          <span className="text-xs text-on-surface-variant truncate">Gespräche</span>
          <span className="font-semibold text-sm truncate">{widget.stats.conversations}</span>
        </div>
        <div className="flex flex-col min-w-0 @[16rem]:border-l @[16rem]:border-outline-variant/30 @[16rem]:pl-2">
          <span className="text-xs text-on-surface-variant truncate">Bewertung</span>
          <span className="font-semibold text-sm truncate">{rating} / 5</span>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-2 mt-auto @[20rem]:grid-cols-3">
        {footerActions.map((action) => (
          <Button key={action.path} asChild variant="outline" size="sm" className="w-full min-w-0">
            <Link to={`/widgets/${widget.id}${action.path}`}>
              <action.icon className="text-sm" width="1em" height="1em" aria-hidden />
              <span className="truncate">{action.label}</span>
            </Link>
          </Button>
        ))}
      </div>
    </Card>
  );
}
