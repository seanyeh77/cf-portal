import { request } from '@playwright/test';
// @ts-expect-error plain JS helper shared with manual runs
import { seed } from './seed.mjs';

export default async function globalSetup() {
  const r = await request.newContext();
  await seed(r);
  await r.storageState({ path: 'e2e/.auth.json' });
  await r.dispose();
}
