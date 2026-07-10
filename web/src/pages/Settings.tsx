import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router';
import { QRCodeSVG } from 'qrcode.react';
import AppShell from '../layouts/AppShell';
import { Heading, Subheading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Button } from '../components/ui/button';
import { Alert, AlertBody, AlertTitle } from '../components/ui/alert';
import { Badge } from '../components/ui/badge';
import { Input } from '../components/ui/input';
import { Field, Label, ErrorMessage } from '../components/ui/fieldset';
import { api, APIError } from '../api/client';
import { useAuthStore } from '../stores/auth';
import { useMeStore } from '../stores/me';

interface UpdateCheck {
  current: string;
  latest: string;
  has_update: boolean;
}

interface TgbotStatus {
  enabled: boolean;
  admin_count: number;
  token_masked?: string;
}

interface IOSProfile {
  url: string;
  port?: number;
  uuid?: string;
}

interface HealthPayload {
  version?: string;
  status?: string;
}

function Section({
  tint,
  title,
  description,
  children,
}: {
  tint: string;
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="glass tile relative overflow-hidden p-6">
      <div
        aria-hidden
        className="absolute inset-x-0 -top-8 h-24 opacity-70 blur-2xl"
        style={{ background: `radial-gradient(60% 100% at 30% 0%, ${tint}, transparent 70%)` }}
      />
      <div className="relative">
        <Subheading>{title}</Subheading>
        {description && <Text className="mt-2">{description}</Text>}
        <div className="mt-4">{children}</div>
      </div>
    </div>
  );
}

function UpgradeSection() {
  const [check, setCheck] = useState<UpdateCheck | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [banner, setBanner] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const c = await api.get<UpdateCheck>('/api/v1/update/check');
        if (!cancelled) setCheck(c);
      } catch (e) {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  async function pollHealth(expected: string): Promise<boolean> {
    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const h = await api.get<HealthPayload>('/api/v1/health');
        if (h?.version && h.version === expected) return true;
        if (h?.version && expected && h.version !== check?.current) return true;
      } catch {
        // /health may error transiently during restart — keep polling.
      }
    }
    return false;
  }

  async function apply() {
    if (!check?.has_update) return;
    setErr(null);
    setSuccess(null);
    setApplying(true);
    setBanner('升级中，服务将重启，请稍候…');
    try {
      await api.post('/api/v1/update/apply');
      // Server responded before restart — attempt one health probe to refresh.
      const ok = await pollHealth(check.latest);
      if (ok) {
        setSuccess(`已升级到 ${check.latest}`);
        try {
          const c = await api.get<UpdateCheck>('/api/v1/update/check');
          setCheck(c);
        } catch { /* ignore */ }
      } else {
        setSuccess('升级请求已提交，请手动刷新页面确认版本。');
      }
    } catch (e) {
      // Connection drop during restart is expected → poll health.
      const isNetErr =
        e instanceof TypeError ||
        (e instanceof APIError && (e.status === 0 || e.status === 502 || e.status === 503));
      if (isNetErr) {
        const ok = await pollHealth(check.latest);
        if (ok) {
          setSuccess(`已升级到 ${check.latest}`);
          try {
            const c = await api.get<UpdateCheck>('/api/v1/update/check');
            setCheck(c);
          } catch { /* ignore */ }
        } else {
          setErr('60 秒内未检测到服务恢复，请手动检查后端状态。');
        }
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setApplying(false);
      setBanner(null);
    }
  }

  return (
    <Section
      tint="rgb(99 102 241 / 0.35)"
      title="升级"
      description="检查并应用最新版本。升级期间服务将重启，页面会短暂断开。"
    >
      {loading && <Text>正在检查版本…</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>操作失败</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {success && (
        <div className="mb-3">
          <Alert open onClose={() => setSuccess(null)}>
            <AlertTitle>升级完成</AlertTitle>
            <AlertBody>{success}</AlertBody>
          </Alert>
        </div>
      )}
      {banner && (
        <div className="mb-3 rounded-lg border border-indigo-500/40 bg-indigo-500/10 px-3 py-2 text-sm text-indigo-900 dark:text-indigo-200">
          {banner}
        </div>
      )}
      {check && (
        <>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-zinc-500">当前版本</dt>
            <dd className="font-mono">{check.current || '未知'}</dd>
            <dt className="text-zinc-500">最新版本</dt>
            <dd className="font-mono">
              {check.latest || '未知'}
              {check.has_update && <Badge color="amber" className="ml-2">有更新</Badge>}
              {!check.has_update && <Badge color="lime" className="ml-2">已是最新</Badge>}
            </dd>
          </dl>
          <div className="mt-4 flex items-center gap-3">
            <Button
              color="indigo"
              onClick={apply}
              disabled={!check.has_update || applying}
            >
              {applying ? '升级中…' : '一键升级'}
            </Button>
            {!check.has_update && <Text>已是最新版本</Text>}
          </div>
        </>
      )}
    </Section>
  );
}

function TgbotSection() {
  const [status, setStatus] = useState<TgbotStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const s = await api.get<TgbotStatus>('/api/v1/settings/tgbot');
        if (!cancelled) setStatus(s);
      } catch (e) {
        if (!cancelled) {
          if (e instanceof APIError && (e.status === 404 || e.status === 501)) {
            setUnavailable(true);
          } else {
            setUnavailable(true);
          }
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  return (
    <Section
      tint="rgb(20 184 166 / 0.35)"
      title="TG Bot"
      description="Telegram 通知机器人状态。修改需编辑 config.yaml 并重启。"
    >
      {loading && <Text>加载中…</Text>}
      {!loading && unavailable && (
        <div className="rounded-lg border border-zinc-500/30 bg-zinc-500/5 px-3 py-2 text-sm text-zinc-700 dark:text-zinc-300">
          后端未暴露 TG Bot 状态接口，请查看 config.yaml 中 <span className="font-mono">tgbot</span> 段。
        </div>
      )}
      {!loading && !unavailable && status && (
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 text-sm">
          <dt className="text-zinc-500">状态</dt>
          <dd>
            {status.enabled
              ? <Badge color="lime">已启用</Badge>
              : <Badge color="zinc">未启用</Badge>}
          </dd>
          <dt className="text-zinc-500">管理员数量</dt>
          <dd className="font-mono">{status.admin_count}</dd>
          {status.token_masked && (
            <>
              <dt className="text-zinc-500">Token</dt>
              <dd className="font-mono">{status.token_masked}</dd>
            </>
          )}
        </dl>
      )}
      <div className="mt-4 text-xs text-zinc-500">
        修改需编辑 <span className="font-mono">config.yaml</span> 并重启服务。
      </div>
    </Section>
  );
}

function IOSSection() {
  const [profile, setProfile] = useState<IOSProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const p = await api.get<IOSProfile>('/api/v1/ios/profile-url');
        if (!cancelled) setProfile(p);
      } catch (e) {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  async function copy() {
    if (!profile?.url) return;
    try {
      await navigator.clipboard.writeText(profile.url);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  function downloadHref(url: string): string {
    if (!url) return '#';
    if (/\.mobileconfig(\?|$)/.test(url)) return url;
    return url.replace(/\/$/, '') + '/ios-dot.mobileconfig';
  }

  return (
    <Section
      tint="rgb(168 85 247 / 0.35)"
      title="iOS 描述文件"
      description="扫描二维码或复制链接到 Safari 打开以安装配置。"
    >
      {loading && <Text>加载中…</Text>}
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>无法获取配置链接</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      {profile?.url && (
        <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
          <div className="rounded-xl bg-white p-3 shadow-sm ring-1 ring-zinc-200 dark:ring-white/10">
            <QRCodeSVG value={profile.url} size={160} level="M" />
          </div>
          <div className="flex-1 space-y-3">
            <div className="break-all rounded-lg bg-zinc-500/5 px-3 py-2 font-mono text-xs text-zinc-800 dark:text-zinc-200">
              {profile.url}
            </div>
            <div className="flex flex-wrap gap-2">
              <Button color="indigo" onClick={copy}>{copied ? '已复制' : '复制链接'}</Button>
              <Button href={downloadHref(profile.url)} color="zinc">下载 .mobileconfig</Button>
            </div>
            {profile.uuid && (
              <div className="text-xs text-zinc-500">
                UUID <span className="font-mono">{profile.uuid}</span>
                {profile.port ? <> · Port <span className="font-mono">{profile.port}</span></> : null}
              </div>
            )}
          </div>
        </div>
      )}
      {!loading && !profile?.url && !err && (
        <div className="text-sm text-zinc-500">
          后端未返回配置链接。扫描请手动输入或使用系统二维码工具。
        </div>
      )}
    </Section>
  );
}

function PasswordSection() {
  const nav = useNavigate();
  const clearAuth = useAuthStore((s) => s.clear);
  const clearMe = useMeStore((s) => s.clear);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [fieldErr, setFieldErr] = useState<{ next?: string; confirm?: string; current?: string }>({});

  function validate(): boolean {
    const fe: { next?: string; confirm?: string; current?: string } = {};
    if (next.length < 8) fe.next = '新密码至少 8 位';
    if (next !== confirm) fe.confirm = '两次输入的新密码不一致';
    if (current && next && current === next) fe.next = '新密码不能与当前密码相同';
    setFieldErr(fe);
    return Object.keys(fe).length === 0;
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    if (!validate()) return;
    setSubmitting(true);
    try {
      await api.post('/api/v1/password', { current, next });
      clearMe();
      clearAuth();
      nav('/login', { replace: true });
    } catch (e) {
      if (e instanceof APIError) {
        if (e.status === 401) setErr('当前密码错误');
        else setErr(e.message || '修改失败');
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Section
      tint="rgb(239 68 68 / 0.30)"
      title="修改密码"
      description="修改成功后当前会话将被清除，需重新登录。"
    >
      {err && (
        <div className="mb-3">
          <Alert open onClose={() => setErr(null)}>
            <AlertTitle>修改失败</AlertTitle>
            <AlertBody>{err}</AlertBody>
          </Alert>
        </div>
      )}
      <form onSubmit={submit} className="space-y-5">
        <Field>
          <Label>当前密码</Label>
          <Input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            autoComplete="current-password"
            required
          />
          {fieldErr.current && <ErrorMessage>{fieldErr.current}</ErrorMessage>}
        </Field>
        <Field>
          <Label>新密码</Label>
          <Input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            autoComplete="new-password"
            required
          />
          {fieldErr.next && <ErrorMessage>{fieldErr.next}</ErrorMessage>}
        </Field>
        <Field>
          <Label>确认新密码</Label>
          <Input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            required
          />
          {fieldErr.confirm && <ErrorMessage>{fieldErr.confirm}</ErrorMessage>}
        </Field>
        <div>
          <Button type="submit" color="indigo" disabled={submitting}>
            {submitting ? '提交中…' : '修改密码'}
          </Button>
        </div>
      </form>
    </Section>
  );
}

export default function Settings() {
  return (
    <AppShell>
      <div className="mb-6">
        <Heading>Settings</Heading>
        <Text className="mt-1">系统升级、Telegram 通知、iOS 描述文件、账户密码。</Text>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <UpgradeSection />
        <TgbotSection />
        <IOSSection />
        <PasswordSection />
      </div>
    </AppShell>
  );
}
