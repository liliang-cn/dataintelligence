import { useState, type ReactNode } from 'react';
import { Link, useLocation } from 'wouter';
import { useTheme } from '@/lib/theme';
import {
  ArrowLeftRightIcon, FolderOpenIcon, GaugeIcon, LogOutIcon, MessagesSquareIcon, MonitorCogIcon, MoonIcon, SunIcon,
} from 'lucide-react';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { Brand } from '@/components/brand';
import { Panel } from '@/components/panel';
import { TokenForm } from '@/components/sign-in';
import { signOut } from '@/lib/api';
import { useSession } from '@/lib/session';
import { cn } from '@/lib/utils';
import { roleName } from '@/lib/words';

export const NAV = [
  { href: '/app/consult', label: '咨询档案', icon: FolderOpenIcon },
  { href: '/app/chat', label: '顾问对话', icon: MessagesSquareIcon },
  { href: '/app/metrics', label: '指标', icon: GaugeIcon },
] as const;

function useActive() {
  const [loc] = useLocation();
  return (href: string) => loc === href || loc.startsWith(`${href}/`) || (href === '/app/consult' && (loc === '/' || loc === '/app'));
}

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const dark = resolvedTheme === 'dark';
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant="ghost" size="icon" className="size-9 rounded-xl" onClick={() => setTheme(dark ? 'light' : 'dark')} aria-label={dark ? '换成浅色' : '换成深色'}>
          {dark ? <SunIcon className="size-[18px]" /> : <MoonIcon className="size-[18px]" />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{dark ? '换成浅色' : '换成深色'}</TooltipContent>
    </Tooltip>
  );
}

function UserMenu() {
  const { me, refresh } = useSession();
  const [switching, setSwitching] = useState(false);
  if (!me) return null;
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" className="flex items-center gap-2.5 rounded-xl py-1 pr-1 pl-1 outline-none transition-colors hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/40 md:pr-3" aria-label="账号">
            <Avatar className="size-8">
              <AvatarFallback className="bg-primary text-[13px] font-bold text-primary-foreground">{me.user.slice(0, 1)}</AvatarFallback>
            </Avatar>
            <span className="grid text-left leading-tight max-md:hidden">
              <b className="text-[13.5px]">{me.user}</b>
              <small className="text-[11.5px] text-muted-foreground">{roleName(me.role)}</small>
            </span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60 rounded-2xl p-2 shadow-float">
          <DropdownMenuLabel className="flex items-center gap-3 px-2 py-2">
            <Avatar className="size-10">
              <AvatarFallback className="bg-primary text-base font-bold text-primary-foreground">{me.user.slice(0, 1)}</AvatarFallback>
            </Avatar>
            <span className="grid leading-tight">
              <b className="text-sm text-foreground">{me.user}</b>
              <small className="text-xs font-normal text-muted-foreground">{roleName(me.role)}</small>
            </span>
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem className="gap-2.5 rounded-lg p-2" onSelect={() => setSwitching(true)}>
            <ArrowLeftRightIcon /> 换一个人登录
          </DropdownMenuItem>
          <DropdownMenuItem className="gap-2.5 rounded-lg p-2" asChild>
            <a href="/ui/">
              <MonitorCogIcon /> 工程师控制台
            </a>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            className="gap-2.5 rounded-lg p-2 text-destructive focus:text-destructive"
            onSelect={async () => {
              signOut();
              await refresh();
            }}
          >
            <LogOutIcon className="text-destructive" /> 退出登录
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Panel open={switching} onOpenChange={setSwitching} title="换一个人登录">
        <TokenForm onDone={() => setSwitching(false)} />
      </Panel>
    </>
  );
}

export function Shell({ children }: { children: ReactNode }) {
  const { info } = useSession();
  const active = useActive();
  return (
    <div className="min-h-dvh bg-stage">
      <header className="sticky top-0 z-40 border-b bg-background/85 backdrop-blur-md supports-[backdrop-filter]:bg-background/75">
        <div className="mx-auto flex h-16 max-w-[1240px] items-center gap-3 px-4 md:gap-6 md:px-6">
          <Link href="/app/consult" className="flex min-w-0 items-center gap-2.5 rounded-lg outline-none focus-visible:ring-3 focus-visible:ring-ring/40">
            <Brand />
            <span className="truncate text-[17px] font-extrabold tracking-tight">{info.title}</span>
          </Link>
          <nav className="flex items-center gap-1 max-md:hidden" aria-label="主导航">
            {NAV.map(({ href, label, icon: Icon }) => (
              <Link
                key={href}
                href={href}
                className={cn(
                  'flex h-9 items-center gap-2 rounded-xl px-3.5 text-[14px] font-semibold text-muted-foreground transition-colors outline-none hover:bg-muted hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/40',
                  active(href) && 'bg-accent text-accent-foreground hover:bg-accent hover:text-accent-foreground',
                )}
              >
                <Icon className="size-4" />
                {label}
              </Link>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-1.5">
            <ThemeToggle />
            <UserMenu />
          </div>
        </div>
      </header>
      <div className="pb-[calc(76px+env(safe-area-inset-bottom))] md:pb-0">{children}</div>
      <nav
        className="fixed inset-x-0 bottom-0 z-40 grid grid-cols-3 border-t bg-background/92 pb-[env(safe-area-inset-bottom)] backdrop-blur-md md:hidden"
        aria-label="主导航"
      >
        {NAV.map(({ href, label, icon: Icon }) => (
          <Link
            key={href}
            href={href}
            className={cn('flex h-16 flex-col items-center justify-center gap-1 text-[11.5px] font-semibold text-muted-foreground', active(href) && 'text-primary')}
          >
            <span className={cn('grid h-7 w-12 place-items-center rounded-full transition-colors', active(href) && 'bg-accent')}>
              <Icon className="size-[19px]" />
            </span>
            {label}
          </Link>
        ))}
      </nav>
    </div>
  );
}
