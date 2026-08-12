import { Outlet, useLocation } from "react-router-dom";
import { ExternalLink, LogOut } from "lucide-react";
import {
  AppShellLayout,
  DropdownMenuItem,
  Logo,
  SidebarUserMenu,
} from "@ki4jlu/design-system";
import { useAuth } from "../auth/AuthContext";
import { useCurrentUser } from "../hooks/useCurrentUser";
import { resolveWidgetPortalUrl } from "../lib/widgetPortal";
import { AppSidebarNav } from "./AppSidebarNav";

// App-Shell für alle geschützten Routen: Layout-Route mit <Outlet/>.
// Ersetzt AuthenticatedLayout + Sidebar + TopAppBar + BottomNavBar — Desktop-
// Sidebar und Mobile-Drawer kommen komplett aus dem Design-System.
function currentPageLabel(pathname: string): string {
  if (pathname.startsWith("/agents")) return "Agenten";
  if (pathname.startsWith("/statistiken")) return "Statistiken";
  return "Konnektoren";
}

export function AppLayout() {
  const { logout } = useAuth();
  const user = useCurrentUser();
  const isAdmin = user?.role === "admin" || user?.role === "superadmin";
  const widgetPortalUrl = resolveWidgetPortalUrl();
  const location = useLocation();

  return (
    <AppShellLayout
      logo={<Logo product="CampusAgents" size="sm" />}
      pageLabel={currentPageLabel(location.pathname)}
      nav={<AppSidebarNav />}
      sidebarFooter={
        <SidebarUserMenu
          initials={user?.initials ?? "?"}
          name={user?.displayName ?? "Benutzer"}
          role={user?.role ?? "authentifiziert"}
        >
          {isAdmin && (
            <DropdownMenuItem asChild>
              <a href={widgetPortalUrl} target="_blank" rel="noopener noreferrer">
                <ExternalLink width="1em" height="1em" aria-hidden />
                Mock-Widget-Portal
              </a>
            </DropdownMenuItem>
          )}
          <DropdownMenuItem variant="destructive" onSelect={logout}>
            <LogOut width="1em" height="1em" aria-hidden />
            Abmelden
          </DropdownMenuItem>
        </SidebarUserMenu>
      }
    >
      <Outlet />
    </AppShellLayout>
  );
}
