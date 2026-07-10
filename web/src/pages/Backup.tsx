import { useEffect, useRef, useState } from 'react';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { Badge } from '../components/ui/badge';
import { Dialog, DialogActions, DialogBody, DialogDescription, DialogTitle } from '../components/ui/dialog';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth';
import type { BackupImportResult, Rule } from '../api/client';

function healthColor(health: string): 'lime' | 'amber' | 'red' | 'zinc' {
  switch (health) {
    case 'confirmed': return 'lime';
    case 'observing': return 'amber';
    case 'rolled_back': return 'red';
    default: return 'zinc';
  }
}

function healthLabel(health: string): string {
  switch (health) {
    case 'confirmed': return '已确认';
    case 'observing': return '健康观察中';
    case 'rolled_back': return '已回滚';
    default: return health || '未知';
  }
}

async function countRulesInFile(file: File): Promise<number | null> {
  try {
    const buf = await file.arrayBuffer();
    const ds = new DecompressionStream('gzip');
    const stream = new Response(buf).body?.pipeThrough(ds);
    if (!stream) return null;
    const bytes = new Uint8Array(await new Response(stream).arrayBuffer());
    // tar: find "rules/active.yaml" header, then read the next 512-aligned block.
    const dec = new TextDecoder();
    for (let off = 0; off + 512 <= bytes.length; off += 512) {
      const name = dec.decode(bytes.slice(off, off + 100)).replace(/\0.*$/, '');
      if (name !== 'rules/active.yaml') continue;
      const sizeOct = dec.decode(bytes.slice(off + 124, off + 136)).replace(/\0.*$/, '').trim();
      const size = parseInt(sizeOct, 8);
      if (!Number.isFinite(size) || size <= 0) return null;
      const yaml = dec.decode(bytes.slice(off + 512, off + 512 + size));
      // Rough count: lines starting with `- id:` — matches the schema shape
      // used by internal/rules serialization.
      const matches = yaml.match(/^\s*-\s*id\s*:/gm);
      return matches ? matches.length : 0;
    }
    return null;
  } catch {
    return null;
  }
}

export default function Backup() {
  const token = useAuthStore((s) => s.token);
  const [err, setErr] = useState<string | null>(null);
  const [importResult, setImportResult] = useState<BackupImportResult | null>(null);
  const [importing, setImporting] = useState(false);
  const [pending, setPending] = useState<{ file: File; targetCount: number | null } | null>(null);
  const [currentCount, setCurrentCount] = useState<number | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    (async () => {
      try {
        const { rules } = await api.get<{ rules: Rule[] }>('/api/v1/rules');
        setCurrentCount(rules.length);
      } catch {
        setCurrentCount(null);
      }
    })();
  }, []);

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

  async function stagedFile(file: File) {
    setErr(null);
    setImportResult(null);
    const targetCount = await countRulesInFile(file);
    setPending({ file, targetCount });
  }

  async function confirmImport() {
    if (!pending) return;
    const file = pending.file;
    setPending(null);
    setImporting(true);
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
      if (body.applied) {
        try {
          const { rules } = await api.get<{ rules: Rule[] }>('/api/v1/rules');
          setCurrentCount(rules.length);
        } catch {
          /* keep old count */
        }
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setImporting(false);
      if (fileRef.current) fileRef.current.value = '';
    }
  }

  function cancelImport() {
    setPending(null);
    if (fileRef.current) fileRef.current.value = '';
  }

  const applyResult = importResult?.apply_result;

  return (
    <AppShell>
      <div className="mb-6">
        <Heading>Backup</Heading>
        <Text className="mt-1">Export a tar.gz of the active rule set plus snapshot metadata · import applies the bundle to the data plane after dry-run and health checks.</Text>
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
              <div>{importResult.entries} entries · {importResult.total_bytes} bytes</div>
              {importResult.applied_snapshot_id !== undefined && (
                <div className="mt-1">Snapshot #{importResult.applied_snapshot_id}</div>
              )}
              {applyResult && (
                <div className="mt-2 flex items-center gap-2">
                  <span>Apply state:</span>
                  <Badge color={healthColor(applyResult.health)}>{healthLabel(applyResult.health)}</Badge>
                  {applyResult.rolled_back && <Badge color="red">rolled back</Badge>}
                </div>
              )}
              {applyResult?.reason && (
                <div className="mt-1 text-sm text-red-700 dark:text-red-400">Reason: {applyResult.reason}</div>
              )}
              {importResult.note ? <div className="mt-1 text-sm">{importResult.note}</div> : null}
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
            <Heading level={3}>Import</Heading>
            <Text className="mt-2">Uploads apply the bundled rules immediately. A confirmation dialog previews the change before anything runs.</Text>
            <div className="mt-4">
              <input
                ref={fileRef}
                type="file"
                accept=".tar.gz,application/gzip"
                className="block w-full text-sm text-zinc-600 file:mr-3 file:rounded-lg file:border-0 file:bg-white/50 file:px-3 file:py-1.5 file:text-zinc-900 file:backdrop-blur hover:file:bg-white/70 dark:text-zinc-300 dark:file:bg-white/10 dark:file:text-zinc-100 dark:hover:file:bg-white/20"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) stagedFile(f);
                }}
                disabled={importing}
              />
              {importing && <Text className="mt-2">Applying…</Text>}
            </div>
          </div>
        </div>
      </div>

      <Dialog open={pending !== null} onClose={cancelImport} size="md">
        <DialogTitle>确认导入备份</DialogTitle>
        <DialogDescription>
          导入将立即应用该规则集到数据面（经 dry-run/健康检查，失败自动回滚）。
        </DialogDescription>
        <DialogBody>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-zinc-500">File</dt>
            <dd className="font-medium">{pending?.file.name}</dd>
            <dt className="text-zinc-500">Size</dt>
            <dd>{pending ? `${pending.file.size} bytes` : ''}</dd>
            <dt className="text-zinc-500">Current active rules</dt>
            <dd>{currentCount !== null ? `${currentCount} rules` : 'unknown'}</dd>
            <dt className="text-zinc-500">Incoming rules</dt>
            <dd>
              {pending?.targetCount !== null && pending?.targetCount !== undefined
                ? `${pending.targetCount} rules`
                : 'unable to preview (non-standard tarball layout)'}
            </dd>
            {currentCount !== null && pending?.targetCount !== null && pending?.targetCount !== undefined && (
              <>
                <dt className="text-zinc-500">Diff</dt>
                <dd className="font-mono">
                  {currentCount} → {pending.targetCount}
                  {' '}
                  <span className="text-zinc-500">
                    ({pending.targetCount - currentCount >= 0 ? '+' : ''}
                    {pending.targetCount - currentCount})
                  </span>
                </dd>
              </>
            )}
          </dl>
        </DialogBody>
        <DialogActions>
          <Button plain onClick={cancelImport}>取消</Button>
          <Button color="indigo" onClick={confirmImport}>确认导入并应用</Button>
        </DialogActions>
      </Dialog>
    </AppShell>
  );
}
