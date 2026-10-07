import { useState } from 'react';
import { KeyRoundIcon, LoaderCircleIcon } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Brand } from '@/components/brand';
import { api, ApiError, setDevUser, setToken, type Me } from '@/lib/api';
import { useSession } from '@/lib/session';
import { roleName } from '@/lib/words';

/** Paste a token (or, on an open dev server, a name) and become that person. */
export function TokenForm({ onDone }: { onDone?: () => void }) {
  const { info, refresh } = useSession();
  const open = info.auth === 'open';
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!value.trim()) {
      setError(open ? '写上你的名字' : '粘贴你的访问令牌');
      return;
    }
    setBusy(true);
    setError('');
    // check the token before it replaces the one in use, so a typo signs nobody out
    if (!open) {
      try {
        await api<Me>('/v1/whoami', { headers: { Authorization: `Bearer ${value.trim()}` } });
      } catch (err) {
        setBusy(false);
        setError(err instanceof ApiError && err.status === 401 ? '令牌无效' : `无法验证令牌：${(err as Error).message}`);
        return;
      }
      setToken(value);
    } else {
      setDevUser(value);
    }
    const me = await refresh();
    setBusy(false);
    if (!me) {
      setError('登录失败');
      return;
    }
    setValue('');
    toast.success(`已登录为 ${me.user}`, { description: roleName(me.role) });
    onDone?.();
  }

  return (
    <form onSubmit={submit} className="grid gap-3">
      <label htmlFor="token" className="text-[13px] font-semibold">{open ? '你的名字' : '访问令牌'}</label>
      <div className="relative">
        <KeyRoundIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          id="token"
          autoFocus
          type={open ? 'text' : 'password'}
          autoComplete={open ? 'name' : 'current-password'}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={open ? '例如 张工' : '粘贴部署方发给你的令牌'}
          aria-invalid={!!error}
          className="h-11 rounded-xl pl-9 text-[15px]"
        />
      </div>
      {error && <p className="text-[13px] text-destructive">{error}</p>}
      <Button type="submit" disabled={busy} className="h-11 rounded-xl text-[15px] font-bold">
        {busy && <LoaderCircleIcon className="animate-spin" />}
        登录
      </Button>
    </form>
  );
}

export function SignInScreen() {
  const { info } = useSession();
  return (
    <main className="grid min-h-dvh place-items-center bg-stage px-4 py-10">
      <div className="w-full max-w-[400px]">
        <div className="mb-7 flex flex-col items-center gap-3 text-center">
          <Brand size={44} />
          <h1 className="text-2xl font-extrabold tracking-tight">{info.title}</h1>
        </div>
        <div className="rounded-2xl bg-card p-6 shadow-float">
          <TokenForm />
        </div>
      </div>
    </main>
  );
}
