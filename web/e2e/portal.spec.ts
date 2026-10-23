import { request as pwRequest } from '@playwright/test';
import { expect, test, toast } from './fixtures';
// @ts-expect-error plain JS helper shared with manual runs
import { BASE } from './seed.mjs';

test.describe.configure({ mode: 'serial' });

const card = (page: import('@playwright/test').Page, host: string) => page.getByTestId(`card-${host}.example.org`);

async function settings(body: Record<string, string>) {
  const r = await pwRequest.newContext({ storageState: 'e2e/.auth.json', baseURL: BASE });
  const res = await r.put('/api/settings', { data: body, headers: { 'X-Requested-With': 'cf-portal' } });
  expect(res.ok()).toBeTruthy();
  await r.dispose();
}

test.describe('mouse', () => {
  test('category tabs filter the cards', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.card')).toHaveCount(5);
    await expect(page.locator('.section-title').first()).toContainText('★ 精選 2');
    await page.locator('.cats').getByRole('button', { name: /^監控/ }).click();
    await expect(page.locator('.card')).toHaveCount(1);
    await expect(card(page, 'grafana')).toBeVisible();
    await page.locator('.cats').getByRole('button', { name: /^全部/ }).click();
    await expect(page.locator('.card')).toHaveCount(5);
  });

  test('search narrows the list', async ({ page }) => {
    await page.goto('/');
    await page.getByLabel('搜尋服務').fill('jupy');
    await expect(page.locator('.card')).toHaveCount(1);
    await expect(card(page, 'jupyter')).toContainText('JupyterHub');
    await page.getByLabel('搜尋服務').fill('不存在的服務');
    await expect(page.getByText('無符合項目')).toBeVisible();
  });

  test('cards show status bars, state and tags; status is polled', async ({ page }) => {
    await page.goto('/');
    const g = card(page, 'gitlab');
    await expect(g.locator('.sbar .cell')).toHaveCount(48);
    await expect(g).toContainText('需帳號 · 成員限定');
    await expect(g).toContainText('正常');
    await expect(g).toHaveAttribute('target', '_blank');
    await expect(card(page, 'jupyter')).toContainText('無回應');
    await expect(page.locator('.live.warn')).toBeVisible();
    const req = await page.waitForRequest('**/status.json', { timeout: 30_000 });
    expect(req.method()).toBe('GET');
  });

  test('announcement can be dismissed and stays dismissed until it changes', async ({ page }) => {
    await settings({ announce_text: '週六 02:00 維護', announce_level: 'warn' });
    await page.goto('/');
    const bar = page.locator('.announce');
    await expect(bar).toContainText('NOTICE');
    await expect(bar).toContainText('週六 02:00 維護');
    await bar.getByRole('button', { name: '關閉公告' }).click();
    await expect(bar).toHaveCount(0);
    await page.reload();
    await expect(page.locator('.announce')).toHaveCount(0);
    await settings({ announce_text: '週日 03:00 維護' });
    await page.reload();
    await expect(page.locator('.announce')).toContainText('週日 03:00 維護');
    await settings({ announce_text: '' });
  });

  test('theme toggle cycles and is remembered', async ({ page }) => {
    await page.goto('/');
    const btn = page.getByRole('button', { name: /^主題：/ });
    await expect(btn).toHaveText('主題：自動');
    await btn.click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await btn.click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await page.getByRole('button', { name: /^主題：/ }).click();
    await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.+/);
  });

  test('no horizontal scrolling at 390 px; keyboard hints hidden', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    for (const path of ['/', '/request', '/login']) {
      await page.goto(path);
      const [sw, cw] = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
      expect(sw, path).toBeLessThanOrEqual(cw);
    }
    await page.goto('/');
    await expect(page.locator('.search .keys')).toBeHidden();
  });

  test('request form shows errors above the form', async ({ page }) => {
    await page.goto('/request');
    await page.getByLabel('姓名').fill('王');
    await page.getByLabel('聯絡方式').fill('x@example.com');
    await page.getByLabel('網址名稱').fill('Bad_Name');
    await page.getByRole('textbox', { name: 'IPv4 位址' }).fill('10.0.0.1');
    await page.getByLabel('用途').fill('測試');
    await page.getByRole('button', { name: '送出' }).click();
    await expect(page.locator('.errbox')).toContainText('名稱格式不符：');
    await expect(page.getByLabel('姓名')).toHaveValue('王');
  });

  test('login page fields work with password managers', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByLabel('帳號')).toHaveAttribute('autocomplete', 'username');
    await expect(page.getByLabel('密碼')).toHaveAttribute('autocomplete', 'current-password');
    await page.getByLabel('帳號').fill('admin');
    await page.getByLabel('密碼').fill('wrong-password');
    await page.getByRole('button', { name: '登入' }).click();
    await expect(page.locator('.errbox')).toContainText('帳號或密碼錯誤');
  });
});

test.describe('keyboard', () => {
  test.use({ viewport: { width: 1400, height: 900 } });

  test('j k h l, gg G, n N move the selection by layout', async ({ page }) => {
    await page.goto('/');
    const sel = page.locator('.card.selected');
    await page.keyboard.press('j');
    await expect(sel).toHaveAttribute('data-card', '0');
    await page.keyboard.press('l');
    await expect(sel).toHaveAttribute('data-card', '1');
    await page.keyboard.press('h');
    await expect(sel).toHaveAttribute('data-card', '0');
    await page.keyboard.press('j');
    await expect(sel).toHaveAttribute('data-card', '2');
    await page.keyboard.press('k');
    await expect(sel).toHaveAttribute('data-card', '0');
    await page.keyboard.press('G');
    await expect(sel).toHaveAttribute('data-card', '4');
    await page.keyboard.press('g');
    await page.keyboard.press('g');
    await expect(sel).toHaveAttribute('data-card', '0');
    await page.keyboard.press('n');
    await expect(sel).toHaveAttribute('data-card', '1');
    await page.keyboard.press('N');
    await expect(sel).toHaveAttribute('data-card', '0');
    await page.keyboard.press('ArrowRight');
    await expect(sel).toHaveAttribute('data-card', '1');
    await page.keyboard.press('Escape');
    await expect(sel).toHaveCount(0);
  });

  test('yy copies and o opens in a new tab', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.goto('/');
    await page.keyboard.press('j');
    await page.keyboard.press('y');
    await page.keyboard.press('y');
    await expect(toast(page, /^已複製 /)).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('/svc/gitlab/');
    const [popup] = await Promise.all([context.waitForEvent('page'), page.keyboard.press('o')]);
    await popup.waitForLoadState();
    expect(popup.url()).toContain('/svc/gitlab/');
    await popup.close();
  });

  test('/ focuses search; Enter selects the first match and keys do not leak', async ({ page, context }) => {
    await page.goto('/');
    await page.keyboard.press('/');
    await expect(page.getByLabel('搜尋服務')).toBeFocused();
    let opened = false;
    context.on('page', () => (opened = true));
    await page.keyboard.type('graf');
    await page.keyboard.press('Enter');
    await expect(page.locator('.card.selected')).toContainText('Grafana');
    expect(opened).toBe(false);
    await page.keyboard.press('/');
    await page.keyboard.press('Escape');
    await expect(page.getByLabel('搜尋服務')).toHaveValue('');
  });

  test(': command line with fuzzy search, set bg, errors and gt', async ({ page, context }) => {
    await page.goto('/');
    await page.keyboard.press(':');
    const cmd = page.getByRole('dialog', { name: '命令列' });
    await expect(cmd).toBeVisible();
    await page.keyboard.type('jphb');
    await expect(cmd.locator('li.sel')).toContainText('JupyterHub');
    await expect(cmd.locator('li.sel mark.hit')).toHaveCount(4);
    const [popup] = await Promise.all([context.waitForEvent('page'), page.keyboard.press('Enter')]);
    await popup.close();
    await expect(cmd).toHaveCount(0);

    await page.keyboard.press(':');
    await page.keyboard.type('set bg=dark');
    await page.keyboard.press('Enter');
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    await page.keyboard.press('Control+k');
    await page.keyboard.type('zzzz');
    await page.keyboard.press('Enter');
    await expect(page.locator('.cmd-err')).toHaveText('E492: Not an editor command: zzzz');
    await page.keyboard.press('Escape');

    await page.keyboard.press(':');
    await page.keyboard.type('set bg=auto');
    await page.keyboard.press('Enter');
    await page.keyboard.press('g');
    await page.keyboard.press('t');
    await expect(page.locator('.cats button.active')).toContainText('教學');
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');
    await expect(page.locator('.cats button.active')).toContainText('全部');
  });

  test('? opens the shortcut help; no vim status line', async ({ page }) => {
    await page.goto('/');
    await page.keyboard.press('?');
    await expect(page.getByTestId('help-dialog')).toContainText('gg / G');
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('help-dialog')).toHaveCount(0);
    await expect(page.getByText('NORMAL')).toHaveCount(0);
  });
});

test.describe('screenshots', () => {
  for (const [w, h, name] of [
    [1400, 900, 'desktop'],
    [390, 844, 'mobile'],
  ] as const) {
    for (const scheme of ['light', 'dark'] as const) {
      test(`${name} ${scheme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme, storageState: 'e2e/.auth.json', locale: 'zh-TW' });
        const page = await ctx.newPage();
        const shot = async (path: string, file: string, ready: string) => {
          await page.goto(path);
          await page.locator(ready).first().waitFor();
          await page.waitForTimeout(300);
          await page.screenshot({ path: `screenshots/${file}-${name}-${scheme}.png`, fullPage: file !== 'records' });
        };
        await shot('/', 'portal', '.card');
        await shot('/request', 'request', '.req-form');
        await shot('/admin#overview', 'overview', '.stats');
        await shot('/admin#records', 'records', '.rec-table');
        if (name === 'desktop') {
          await page.getByTestId('row-chain').locator('td.c-type').click();
          await page.getByTestId('record-drawer').waitFor();
          await page.screenshot({ path: `screenshots/drawer-${name}-${scheme}.png` });
          await page.getByTestId('record-drawer').locator('.drawer-actions').getByRole('button', { name: '編輯' }).click();
          await page.getByTestId('record-editor').waitFor();
          await page.screenshot({ path: `screenshots/editor-${name}-${scheme}.png` });
        }
        await ctx.close();
      });
    }
  }
});
