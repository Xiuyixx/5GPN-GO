import { render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import { api } from '../api/client';
import Snapshots from './Snapshots';

vi.mock('../layouts/AppShell', () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

describe('Snapshots current marker', () => {
  beforeEach(() => {
    vi.spyOn(api, 'get').mockResolvedValue({
      snapshots: [
        {
          id: 3,
          created_at: '2026-01-03T00:00:00Z',
          config_hash: '3333333333333',
          note: 'exit-switch:direct',
          active: false,
          rollbackable: false,
        },
        {
          id: 2,
          created_at: '2026-01-02T00:00:00Z',
          config_hash: '2222222222222',
          active: false,
          rollbackable: true,
        },
        {
          id: 1,
          created_at: '2026-01-01T00:00:00Z',
          config_hash: '1111111111111',
          active: true,
          rollbackable: true,
        },
      ],
    });
  });

  it('uses the backend active flag instead of list position', async () => {
    render(<Snapshots />);
    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(4));
    const rows = screen.getAllByRole('row');
    expect(rows[1]).toHaveTextContent('latest');
    expect(rows[1]).not.toHaveTextContent('current');
    expect(rows[3]).toHaveTextContent('current');
  });

  it('does not offer rollback for an exit-switch snapshot without rules', async () => {
    render(<Snapshots />);
    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(4));
    const rows = screen.getAllByRole('row');
    expect(within(rows[1]).queryByRole('button', { name: 'Roll back' })).not.toBeInTheDocument();
    expect(rows[1]).toHaveTextContent('no rules');
    expect(within(rows[2]).getByRole('button', { name: 'Roll back' })).toBeEnabled();
  });
});
