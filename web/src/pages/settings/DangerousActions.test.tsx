import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../../i18n';
import { api } from '../../api/client';
import { InternalOnlySection } from './InternalOnlySection';
import { MTProxySection } from './MTProxySection';
import { UpgradeSection } from './UpgradeSection';

describe('destructive settings confirmations', () => {
  beforeEach(() => {
    vi.spyOn(api, 'post').mockResolvedValue({});
  });

  it('directs available updates to the external installer without submitting', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({ current: 'v1', latest: 'v2', has_update: true });
    render(<UpgradeSection />);

    expect(await screen.findByText('Install updates through the external installer or privileged supervisor; in-process replacement is disabled.')).toBeVisible();
    expect(screen.getByText('Update available')).toBeVisible();
    expect(screen.queryByRole('button', { name: 'One-click upgrade' })).not.toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();
  });

  it('does not rotate an existing MTProxy secret without confirmation', async () => {
    vi.spyOn(api, 'get').mockImplementation(async (path) => {
      if (path === '/api/v1/settings/panel') return { server: { domain: 'panel.test' } };
      return {
        enabled: true,
        listen: '0.0.0.0:2443',
        secret_configured: true,
        fronting_domain: 'www.cloudflare.com',
        service_status: 'running',
      };
    });
    render(<MTProxySection />);

    fireEvent.click(await screen.findByRole('button', { name: 'Rotate fake-TLS secret' }));
    expect(api.post).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog')).toHaveTextContent('Existing Telegram proxy links will stop working');
  });

  it('does not enable the network allowlist without a lockout warning', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({ enabled: false, cidrs: '10.0.0.0/8' });
    render(<InternalOnlySection />);

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Enable internal-only restriction' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save internal-only settings' }));
    expect(api.post).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog')).toHaveTextContent('this panel session will be disconnected');
  });
});
