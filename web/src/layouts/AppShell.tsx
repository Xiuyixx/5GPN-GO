import type { ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';
import {
  Sidebar,
  SidebarBody,
  SidebarHeader,
  SidebarItem,
  SidebarLabel,
  SidebarSection,
  SidebarSpacer,
} from '../components/ui/sidebar';
import { SidebarLayout } from '../components/ui/sidebar-layout';
import { Navbar, NavbarItem, NavbarSection, NavbarSpacer } from '../components/ui/navbar';
import { Heading } from '../components/ui/heading';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth';

interface Item {
  label: string;
  to: string;
}

const NAV: Item[] = [
  { label: 'Dashboard', to: '/' },
  { label: 'Rules', to: '/rules' },
  { label: 'Exits', to: '/exits' },
  { label: 'Snapshots', to: '/snapshots' },
  { label: 'Backup', to: '/backup' },
  { label: 'Logs', to: '/logs' },
];

interface Props {
  children: ReactNode;
}

export default function AppShell({ children }: Props) {
  const nav = useNavigate();
  const location = useLocation();
  const username = useAuthStore((s) => s.username);
  const clear = useAuthStore((s) => s.clear);

  async function logout() {
    try { await api.post('/api/v1/logout'); } catch { /* ignore */ }
    clear();
    nav('/login', { replace: true });
  }

  return (
    <SidebarLayout
      navbar={
        <Navbar>
          <NavbarSpacer />
          <NavbarSection>
            <NavbarItem onClick={logout}>Log out</NavbarItem>
          </NavbarSection>
        </Navbar>
      }
      sidebar={
        <Sidebar>
          <SidebarHeader>
            <Heading level={2}>5gpn</Heading>
          </SidebarHeader>
          <SidebarBody>
            <SidebarSection>
              {NAV.map((item) => (
                <SidebarItem
                  key={item.to}
                  href={item.to}
                  current={location.pathname === item.to || (item.to === '/' && location.pathname === '/')}
                >
                  <SidebarLabel>{item.label}</SidebarLabel>
                </SidebarItem>
              ))}
            </SidebarSection>
            <SidebarSpacer />
            <SidebarSection>
              <SidebarItem>
                <SidebarLabel>{username ?? 'unknown'}</SidebarLabel>
              </SidebarItem>
              <SidebarItem onClick={logout}>
                <SidebarLabel>Log out</SidebarLabel>
              </SidebarItem>
            </SidebarSection>
          </SidebarBody>
        </Sidebar>
      }
    >
      {children}
    </SidebarLayout>
  );
}

// Re-export react-router Link so page components can use the same anchor
// helper without leaking react-router imports everywhere.
export { Link };
