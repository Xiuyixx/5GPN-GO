import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { describe, expect, it } from 'vitest';
import { Link } from './link';

function Location() {
  return <output>{useLocation().pathname}</output>;
}

describe('Link', () => {
  it('uses the client router for same-origin paths', () => {
    render(
      <MemoryRouter initialEntries={['/from']}>
        <Link href="/to">Continue</Link>
        <Routes>
          <Route path="*" element={<Location />} />
        </Routes>
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('link', { name: 'Continue' }));
    expect(screen.getByText('/to')).toBeInTheDocument();
  });

  it('keeps external URLs as normal anchors', () => {
    render(<Link href="https://example.com/file">Download</Link>);
    expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute(
      'href',
      'https://example.com/file',
    );
  });
});
