import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import '../shared/base.css';
import './portal.css';
import { readInit } from '../shared/api';
import { installTooltips } from '../shared/tooltip';
import { Portal, type PortalInit } from './Portal';
import { ClosedPage, LoginPage, RequestPage, RequestStatusPage, type PublicInit } from './Pages';

installTooltips();
const init = readInit<PublicInit & PortalInit & { page: string }>();
const qc = new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: true, retry: 1 } } });

function Root() {
  switch (init.page) {
    case 'request':
      return <RequestPage init={init} />;
    case 'request_status':
      return <RequestStatusPage init={init} />;
    case 'login':
      return <LoginPage init={init} />;
    case 'closed':
      return <ClosedPage init={init} />;
    default:
      return <Portal init={init} />;
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={qc}>
      <Root />
    </QueryClientProvider>
  </StrictMode>,
);
