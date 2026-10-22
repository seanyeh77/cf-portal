import { test as base, expect, type Page } from '@playwright/test';

/** Fails the test when the page logs a JavaScript error. */
export const test = base.extend<{ jsErrors: string[] }>({
  jsErrors: [
    async ({ page }, use) => {
      const errors: string[] = [];
      page.on('pageerror', (e) => errors.push(e.message));
      page.on('console', (m) => {
        if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) errors.push(m.text());
      });
      await use(errors);
      expect(errors, 'JavaScript errors').toEqual([]);
    },
    { auto: true },
  ],
});
export { expect };

export const row = (page: Page, sub: string) => page.getByTestId('row-' + sub);
export const toast = (page: Page, text: string | RegExp) => page.locator('.toast', { hasText: text });

export function taipeiDate(offsetDays = 0) {
  const d = new Date(Date.now() + offsetDays * 86400000);
  return new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Taipei' }).format(d);
}

export async function openRecords(page: Page) {
  await page.goto('/admin#records');
  await expect(page.locator('.rec-table')).toBeVisible();
}
