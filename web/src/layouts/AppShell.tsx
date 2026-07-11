import { useEffect, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
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
import LanguageSwitcher from '../components/LanguageSwitcher';
import { api } from '../api/client';
import type { Me } from '../api/client';
import { useAuthStore } from '../stores/auth';
import { useMeStore } from '../stores/me';

interface Item {
  labelKey: string;
  to: string;
  icon: string;
}

const NAV: Item[] = [
  { labelKey: 'nav.dashboard', to: '/',          icon: '▨' },
  { labelKey: 'nav.rules',     to: '/rules',     icon: '⌘' },
  { labelKey: 'nav.exits',     to: '/exits',     icon: '⇋' },
  { labelKey: 'nav.snapshots', to: '/snapshots', icon: '⌸' },
  { labelKey: 'nav.backup',    to: '/backup',    icon: '⤓' },
  { labelKey: 'nav.logs',      to: '/logs',      icon: '≡' },
  { labelKey: 'nav.settings',  to: '/settings',  icon: '⚙' },
];

interface Props {
  children: ReactNode;
}

export default function AppShell({ children }: Props) {
  const { t } = useTranslation();
  const nav = useNavigate();
  const location = useLocation();
  const authUsername = useAuthStore((s) => s.username);
  const clear = useAuthStore((s) => s.clear);
  const meUsername = useMeStore((s) => s.username);
  const meLoaded = useMeStore((s) => s.loaded);
  const setMe = useMeStore((s) => s.set);
  const clearMe = useMeStore((s) => s.clear);
  const displayUsername = meUsername ?? authUsername ?? 'unknown';

  useEffect(() => {
    if (meLoaded) return;
    let cancelled = false;
    (async () => {
      try {
        const me = await api.get<Me>('/api/v1/me');
        if (!cancelled) setMe(me.user_id, me.username);
      } catch {
        // 401 already handled by api client (clears token → route guard redirects).
        // Other errors: keep authStore username fallback.
      }
    })();
    return () => { cancelled = true; };
  }, [meLoaded, setMe]);

  async function logout() {
    try { await api.post('/api/v1/logout'); } catch { /* ignore */ }
    clearMe();
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
              <LanguageSwitcher className="mr-4" />
              <NavbarItem onClick={logout}>{t('nav.logout')}</NavbarItem>
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
                  <div className="text-sm font-semibold tracking-tight text-zinc-900 dark:text-zinc-100">{t('app.brand')}</div>
                  <div className="text-[11px] uppercase tracking-widest text-zinc-500">{t('app.tagline')}</div>
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
                    <SidebarLabel>{t(item.labelKey)}</SidebarLabel>
                  </SidebarItem>
                ))}
              </SidebarSection>
              <SidebarSpacer />
              <SidebarSection>
                <SidebarItem>
                  <div className="flex flex-col text-left">
                    <SidebarLabel>{displayUsername}</SidebarLabel>
                    <span className="text-[11px] text-zinc-500">{t('nav.signedIn')}</span>
                  </div>
                </SidebarItem>
                <div className="px-2 pb-1">
                  <LanguageSwitcher />
                </div>
                <SidebarItem onClick={logout}>
                  <SidebarLabel>{t('nav.logout')}</SidebarLabel>
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
