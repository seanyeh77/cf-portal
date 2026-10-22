import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import '../shared/base.css';
import './admin.css';
import { installTooltips } from '../shared/tooltip';
import { queryClient } from './lib/data';
import { App } from './App';

installTooltips();
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
