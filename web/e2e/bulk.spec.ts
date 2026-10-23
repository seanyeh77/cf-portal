// Bulk actions from the records bar and the requests page, mouse only.
// Uses host005-host011, which the other specs leave alone, and takes off the
// portal whatever it lists, so the portal spec still sees its five cards.
import type { Page } from '@playwright/test';
import { expect, openRecords, row, test, toast } from './fixtures';

test.use({ storageState: 'e2e/.auth.json' });
test.describe.configure({ mode: 'serial' });

async function select(page: Page, ...subs: string[]) {
  await openRecords(page);
  for (const s of subs) await row(page, s).locator('.check-hit').click();
  await expect(page.locator('.bulkbar')).toContainText(`已選 ${subs.length} 筆`);
  return page.locator('.bulkbar');
}

test('bulk DNS change previews each record, lists skipped ones and keeps the selection', async ({ page }) => {
  const bar = await select(page, 'host005', 'host006');
  await bar.getByRole('button', { name: '修改 DNS' }).click();
  let dlg = page.getByTestId('bulk-dns');
  await dlg.getByLabel('修改TTL').check();
  await dlg.getByLabel('TTL', { exact: true }).selectOption({ label: '5 min' });
  await dlg.getByRole('button', { name: '預覽變更' }).click();
  let preview = page.getByTestId('bulk-dns-preview');
  await expect(preview.getByTestId('plan-host005')).toContainText('TTL：Auto → 5 min');
  await preview.getByRole('button', { name: '套用 2 筆' }).click();
  await expect(toast(page, '已修改 2 筆，連線檢查中')).toBeVisible();
  await expect(bar).toContainText('已選 2 筆');

  // A TXT record in the selection is listed as not applicable.
  await row(page, '_dmarc').locator('.check-hit').click();
  await bar.getByRole('button', { name: '修改 DNS' }).click();
  dlg = page.getByTestId('bulk-dns');
  await dlg.getByLabel('修改指向目標').check();
  await dlg.getByLabel('指向目標', { exact: true }).fill('10.0.3.250');
  await dlg.getByRole('button', { name: '預覽變更' }).click();
  preview = page.getByTestId('bulk-dns-preview');
  await expect(preview).toContainText('不會變更（1 筆）');
  await expect(preview).toContainText('_dmarc：TXT 不適用指向');
  await preview.getByRole('button', { name: '返回修改' }).click();
  await expect(page.getByTestId('bulk-dns').getByLabel('指向目標', { exact: true })).toHaveValue('10.0.3.250');
  await page.getByTestId('bulk-dns').getByRole('button', { name: '預覽變更' }).click();
  await page.getByTestId('bulk-dns-preview').getByRole('button', { name: '套用 2 筆' }).click();
  await expect(toast(page, '已修改 2 筆')).toBeVisible();
  await expect(row(page, 'host006')).toContainText('10.0.3.250');
  await bar.getByRole('button', { name: '取消選取' }).click();
});

test('bulk listing: list, edit tags, then take off the portal', async ({ page }) => {
  const bar = await select(page, 'host007', 'host008');
  await bar.getByTestId('bulk-portal-menu').click();
  await page.getByRole('menuitem', { name: '上架到入口…（2 筆）' }).click();
  const dlg = page.getByTestId('bulk-list');
  await dlg.getByLabel('分類').fill('測試分類');
  await dlg.getByLabel('顯示名稱 host007.example.org').fill('第七號主機');
  await dlg.getByRole('button', { name: '上架 2 筆' }).click();
  await expect(toast(page, '已上架 2 筆')).toBeVisible();
  await expect(row(page, 'host007')).toContainText('入口：第七號主機');

  await bar.getByTestId('bulk-portal-menu').click();
  await page.getByRole('menuitem', { name: '修改入口設定…（2 筆）' }).click();
  const edit = page.getByTestId('bulk-list-edit');
  await edit.getByRole('button', { name: '+ 內部使用' }).click();
  await expect(edit.getByLabel('加入標籤')).toHaveValue('內部使用');
  await edit.getByRole('button', { name: '儲存' }).click();
  await expect(toast(page, '已更新 2 筆入口設定')).toBeVisible();

  await bar.getByTestId('bulk-portal-menu').click();
  await page.getByRole('menuitem', { name: '下架（2 筆）' }).click();
  await page.getByTestId('confirm-dialog').getByRole('button', { name: '下架' }).click();
  await expect(toast(page, '已下架 2 筆')).toBeVisible();
  await expect(row(page, 'host007')).not.toContainText('入口：');
});

test('bulk mute with a note, check now and export the selection', async ({ page }) => {
  const bar = await select(page, 'host009', 'host010');
  await bar.getByTestId('bulk-meta-menu').click();
  await page.getByRole('menuitem', { name: '忽略問題…' }).click();
  const dlg = page.getByTestId('bulk-mute');
  await dlg.getByText('指向內網位址').click();
  await dlg.getByLabel('原因（選填）').fill('內網服務，可接受');
  await dlg.getByRole('button', { name: '儲存' }).click();
  await expect(toast(page, '已更新 2 筆')).toBeVisible();
  await row(page, 'host009').locator('td.c-target').click();
  const drawer = page.getByTestId('record-drawer');
  await expect(drawer).toContainText('內網服務，可接受');
  await expect(drawer).toContainText('已忽略：指向內網位址');
  await drawer.getByRole('button', { name: '關閉' }).click();

  await bar.getByRole('button', { name: '立即檢查' }).click();
  await expect(toast(page, '2 筆連線檢查中')).toBeVisible();

  const [download] = await Promise.all([page.waitForEvent('download'), bar.getByRole('button', { name: '匯出' }).click()]);
  const csv = await (await download.createReadStream()).toArray();
  const lines = Buffer.concat(csv).toString('utf8').trim().split('\n');
  expect(lines).toHaveLength(3);
  expect(lines[1]).toContain('host009.example.org');
});

test('a custom expiry cycle adds its own quick choice', async ({ page }) => {
  await page.goto('/admin#settings');
  await page.getByLabel('自訂到期週期名稱').fill('學期末');
  await page.getByLabel('週期結束日').fill('06-30, 01-31');
  await page.getByRole('button', { name: '儲存設定' }).click();
  await expect(toast(page, '已儲存')).toBeVisible();
  await expect(page.getByLabel('週期結束日')).toHaveValue('01-31, 06-30');

  const bar = await select(page, 'host011');
  await bar.getByTestId('bulk-meta-menu').click();
  await page.getByRole('menuitem', { name: /^到期日：學期末（\d{4}-(01-31|06-30)）$/ }).click();
  await expect(toast(page, /^已將到期日設為 \d{4}-(01-31|06-30)$/)).toBeVisible();
  await row(page, 'host011').getByRole('button', { name: '編輯' }).click();
  await expect(page.getByRole('dialog').getByRole('button', { name: '學期末' })).toBeVisible();
});

test('approve and reject requests in bulk', async ({ page, browser }) => {
  const pub = await browser.newPage();
  for (const name of ['bulk-a', 'bulk-b']) {
    await pub.goto('/request');
    await pub.getByLabel('姓名').fill('林同仁');
    await pub.getByLabel('聯絡方式').fill('lin@example.com');
    await pub.getByLabel('網址名稱').fill(name);
    await pub.getByText('其他網址').click();
    await pub.getByLabel('目標網址').fill(name + '.pages.dev');
    await pub.getByLabel('用途').fill('專案網站');
    await pub.getByRole('button', { name: '送出' }).click();
    await expect(pub.getByTestId('request-status')).toHaveText('審核中');
  }
  await pub.close();

  await page.goto('/admin#requests');
  await page.getByLabel('選取全部待審申請').check();
  const bar = page.locator('.bulkbar');
  await expect(bar).toContainText('已選 2 筆');
  await bar.getByRole('button', { name: '核准…' }).click();
  const dlg = page.getByTestId('bulk-approve');
  await dlg.locator('label', { hasText: 'bulk-b' }).getByRole('checkbox').uncheck();
  await dlg.getByRole('button', { name: '核准並建立 1 筆' }).click();
  await expect(toast(page, '已核准 1 筆，連線檢查中')).toBeVisible();
  await expect(page.locator('.req-card.approved', { hasText: 'bulk-a' })).toBeVisible();

  await expect(bar).toContainText('已選 1 筆');
  await bar.getByRole('button', { name: '駁回…' }).click();
  const rej = page.getByTestId('bulk-reject');
  await rej.getByLabel('原因（申請人可見）').fill('名稱與既有服務重複');
  await rej.getByRole('button', { name: '駁回 1 筆' }).click();
  await expect(toast(page, '已駁回 1 筆')).toBeVisible();
  await expect(page.locator('.req-card.rejected', { hasText: 'bulk-b' })).toBeVisible();
  await expect(bar).toHaveCount(0);
});
