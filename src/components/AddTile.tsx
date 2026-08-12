import { Link } from "react-router-dom";
import { Plus } from "lucide-react";

// Gestrichelte "Hinzufügen"-Kachel für Karten-Grids. Ersetzt AddWidgetCard
// und die Inline-Kopie auf der Agenten-Seite. Upstream-Kandidat.
export interface AddTileProps {
  to: string;
  label: string;
  /** Erklärender Zusatz unter dem Label. */
  hint?: string;
}

export function AddTile({ to, label, hint }: AddTileProps) {
  return (
    <Link
      to={to}
      className="border-2 border-dashed border-outline-variant rounded-xl p-4 flex flex-col items-center justify-center text-on-surface-variant hover:border-primary hover:text-primary transition-all cursor-pointer group bg-surface-container-low/50"
    >
      <div className="w-12 h-12 rounded-full border-2 border-dashed border-current flex items-center justify-center mb-3 group-hover:scale-110 transition-transform">
        <Plus className="text-[28px]" width="1em" height="1em" aria-hidden />
      </div>
      <span className="font-headline-md text-base font-bold text-center">{label}</span>
      {hint && <p className="text-xs mt-1 opacity-70 text-center">{hint}</p>}
    </Link>
  );
}
