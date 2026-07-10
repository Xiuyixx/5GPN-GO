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
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth';

interface Item {
  label: string;
  to: string;
  icon: string;
}

const NAV: Item[] = [
  { label: 'Dashboard', to: '/',          icon: '▨' },
  { label: 'Rules',     to: '/rules',     icon: '⌘' },
  { label: 'Exits',     to: '/exits',     icon: '⇋' },
  { label: 'Snapshots', to: '/snapshots', icon: '⌸' },
  { label: 'Backup',    to: '/backup',    icon: '⤓' },
  { label: 'Logs',      to: '/logs',      icon: '≡' },
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
    <>
      <div className="ambient-bg" aria-hidden />
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
              <div className="flex items-center gap-3 px-2 py-1">
                <div className="grid h-9 w-9 place-items-center rounded-xl bg-gradient-to-br from-indigo-500 via-violet-500 to-teal-400 text-sm font-bold text-white shadow-lg shadow-indigo-500/30">
                  5G
                </div>
                <div>
                  <div className="text-sm font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">5gpn</div>
                  <div className="text-[11px] uppercase tracking-widest text-zinc-500">personal gateway</div>
                </div>
              </div>
            </SidebarHeader>
            <SidebarBody>
              <SidebarSection>
                {NAV.map((item) => (
                  <SidebarItem
                    key={item.to}
                    href={item.to}
                    current={location.pathname === item.to || (item.to === '/' && location.pathname === '/')}
                  >
                    <span aria-hidden className="mr-1 inline-block w-4 text-zinc-500">{item.icon}</span>
                    <SidebarLabel>{item.label}</SidebarLabel>
                  </SidebarItem>
                ))}
              </SidebarSection>
              <SidebarSpacer />
              <SidebarSection>
                <SidebarItem>
                  <div className="flex flex-col text-left">
                    <SidebarLabel>{username ?? 'unknown'}</SidebarLabel>
                    <span className="text-[11px] text-zinc-500">signed in</span>
                  </div>
                </SidebarItem>
                <SidebarItem onClick={logout}>
                  <SidebarLabel>Log out</SidebarLabel>
                </SidebarItem>
              </SidebarSection>
            </SidebarBody>
          </Sidebar>
        }
      >
        <div className="fade-up mx-auto w-full max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
          {children}
        </div>
      </SidebarLayout>
    </>
  );
}

export { Link };
