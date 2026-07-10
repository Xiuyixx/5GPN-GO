import { useRef, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { useAuthStore } from '../stores/auth';
import type { BackupImportResult } from '../api/client';

export default function Backup() {
  const token = useAuthStore((s) => s.token);
  const [err, setErr] = useState<string | null>(null);
  const [importResult, setImportResult] = useState<BackupImportResult | null>(null);
  const [importing, setImporting] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  async function downloadExport() {
    setErr(null);
    try {
      const res = await fetch('/api/v1/backup/export', {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (!res.ok) {
        throw new Error(`export failed: ${res.status}`);
      }
      const blob = await res.blob();
      const href = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = href;
      a.download = `5gpn-backup-${new Date().toISOString().replace(/[:.]/g, '-')}.tar.gz`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(href);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  async function upload(file: File) {
    setErr(null); setImporting(true); setImportResult(null);
    try {
      const res = await fetch('/api/v1/backup/import', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/gzip',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: file,
      });
      if (!res.ok) throw new Error(`import failed: ${res.status} ${await res.text()}`);
      const body: BackupImportResult = await res.json();
      setImportResult(body);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setImporting(false);
    }
  }

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>Backup</Heading>
        <Text className="mt-1">Export a tar.gz of the active rule set plus snapshot metadata · uploads validate the round-trip.</Text>
      </div>

      {err && (
        <div className="mb-4">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>Something went wrong</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {importResult && (
        <div className="mb-4">
          <Alert open onClose={() => setImportResult(null)}>
            <AlertTitle>Import summary</AlertTitle>
            <AlertBody>
              {importResult.entries} entries · {importResult.total_bytes} bytes
              {importResult.note ? ` · ${importResult.note}` : ''}
            </AlertBody>
          </Alert>
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="glass tile relative overflow-hidden p-6">
          <div
            aria-hidden
            className="absolute inset-x-0 -top-8 h-24 opacity-70 blur-2xl"
            style={{ background: 'radial-gradient(60% 100% at 30% 0%, rgb(99 102 241 / 0.35), transparent 70%)' }}
          />
          <div className="relative">
            <Heading level={3}>Export</Heading>
            <Text className="mt-2">Contains rules/active.yaml, snapshots/manifest.json, and a README.</Text>
            <div className="mt-4">
              <Button color="indigo" onClick={downloadExport}>Download tar.gz</Button>
            </div>
          </div>
        </div>
        <div className="glass tile relative overflow-hidden p-6">
          <div
            aria-hidden
            className="absolute inset-x-0 -top-8 h-24 opacity-70 blur-2xl"
            style={{ background: 'radial-gradient(60% 100% at 30% 0%, rgb(20 184 166 / 0.35), transparent 70%)' }}
          />
          <div className="relative">
            <Heading level={3}>Import (dry-run)</Heading>
            <Text className="mt-2">Uploads to verify structure. Real restore lands in M2 S4.</Text>
            <div className="mt-4">
              <input
                ref={fileRef}
                type="file"
                accept=".tar.gz,application/gzip"
                className="block w-full text-sm text-zinc-600 file:mr-3 file:rounded-lg file:border-0 file:bg-white/50 file:px-3 file:py-1.5 file:text-zinc-900 file:backdrop-blur hover:file:bg-white/70 dark:text-zinc-300 dark:file:bg-white/10 dark:file:text-zinc-100 dark:hover:file:bg-white/20"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) upload(f);
                }}
                disabled={importing}
              />
              {importing && <Text className="mt-2">Validating…</Text>}
            </div>
          </div>
        </div>
      </div>
    </AppShell>
  );
}
