import { lazy, Suspense } from 'react';
import { Redirect, Route, Switch } from 'wouter';
import { LoaderCircleIcon, WifiOffIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Shell } from '@/components/shell';
import { SignInScreen } from '@/components/sign-in';
import { ModelProvider } from '@/lib/model';
import { useSession } from '@/lib/session';
import { ConsultPage } from '@/pages/consult';

// the chat brings the Markdown renderer; neither it nor the metrics list is needed to open the board
const ChatPage = lazy(() => import('@/pages/chat').then((m) => ({ default: m.ChatPage })));
const MetricsPage = lazy(() => import('@/pages/metrics').then((m) => ({ default: m.MetricsPage })));

export function App() {
  const { state, error, refresh } = useSession();
  if (state === 'loading') {
    return (
      <div className="grid min-h-dvh place-items-center bg-stage text-muted-foreground" aria-busy>
        <LoaderCircleIcon className="size-6 animate-spin" />
      </div>
    );
  }
  if (state === 'signed-out') return <SignInScreen />;
  if (state === 'offline') {
    return (
      <div className="grid min-h-dvh place-items-center bg-stage px-4">
        <div className="max-w-sm text-center">
          <WifiOffIcon className="mx-auto size-8 text-muted-foreground" />
          <h1 className="mt-3 text-lg font-extrabold">连不上服务</h1>
          <p className="mt-1.5 text-sm text-muted-foreground">{error}</p>
          <Button className="mt-5 rounded-xl" onClick={() => refresh()}>再试一次</Button>
        </div>
      </div>
    );
  }
  return (
    <ModelProvider enabled>
      <Shell>
        <Suspense fallback={<div className="grid h-60 place-items-center text-muted-foreground"><LoaderCircleIcon className="size-5 animate-spin" /></div>}>
        <Switch>
          <Route path="/app/consult" component={ConsultPage} />
          <Route path="/app/chat" component={ChatPage} />
          <Route path="/app/metrics" component={MetricsPage} />
          <Route path="/">{() => <Redirect to="/app/consult" replace />}</Route>
          <Route>{() => <Redirect to="/app/consult" replace />}</Route>
        </Switch>
        </Suspense>
      </Shell>
    </ModelProvider>
  );
}
