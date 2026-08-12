import { useLocation } from "react-router-dom";
import { Link } from "react-router-dom";
import { Brain, ChartColumn, Waypoints } from "lucide-react";
import { NavItem, Stack } from "@ki4jlu/design-system";

// Navigationsinhalt der App-Shell — AppShellLayout rendert ihn in der
// Desktop-Sidebar UND im Mobile-Drawer.

export function AppSidebarNav() {
  const location = useLocation();

  const agentsActive = location.pathname.startsWith("/agents");
  const widgetsActive = location.pathname === "/" || location.pathname.startsWith("/widgets");
  const statistikenActive = location.pathname.startsWith("/statistiken");

  return (
    <Stack gap="sm">
      <NavItem asChild active={agentsActive}>
        <Link to="/agents">
          <Brain width="1em" height="1em" aria-hidden />
          <span>Agenten</span>
        </Link>
      </NavItem>

      <NavItem asChild active={widgetsActive}>
        <Link to="/">
          <Waypoints width="1em" height="1em" aria-hidden />
          <span>Konnektoren</span>
        </Link>
      </NavItem>

      <NavItem asChild active={statistikenActive}>
        <Link to="/statistiken">
          <ChartColumn width="1em" height="1em" aria-hidden />
          <span>Statistiken</span>
        </Link>
      </NavItem>
    </Stack>
  );
}
