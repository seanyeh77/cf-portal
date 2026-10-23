// Admin console, driven with the mouse only (clicks and form filling).
import { expect, openRecords, row, taipeiDate, test, toast } from './fixtures';

test.use({ storageState: 'e2e/.auth.json' });
test.describe.configure({ mode: 'serial' });

test('overview 查看 opens records with the matching filter', async ({ page }) => {
  await page.goto('/admin');
  await page.getByTestId('ov-bad_target').getByRole('button', { name: '查看' }).click();
  await expect(page).toHaveURL(/#records$/);
  await expect(page.locator('.filter-desc')).toContainText('目前篩選：CNAME 目標無效，符合 1 筆');
  await expect(row(page, 'alias')).toBeVisible();
  await expect(row(page, 'grafana')).toHaveCount(0);
  await page.getByRole('button', { name: '清除全部' }).click();
  await expect(page.locator('.filter-desc')).toHaveCount(0);
  await expect(row(page, 'grafana')).toBeVisible();
});

test('overview 查看 for expiry and severity filters are described', async ({ page }) => {
  await openRecords(page);
  await page.locator('.severity').getByRole('button', { name: /^注意/ }).click();
  await page.getByLabel('到期狀態').selectOption('none');
  await expect(page.locator('.filter-desc')).toContainText('目前篩選：無到期日、注意，符合 1 筆');
  await page.getByRole('button', { name: '清除全部' }).click();
});

test('clicking a row opens the detail drawer', async ({ page }) => {
  await openRecords(page);
  await row(page, 'grafana').locator('td.c-target').click();
  const drawer = page.getByTestId('record-drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer).toContainText('grafana.example.org');
  await expect(drawer).toContainText('DNS 設定');
  await drawer.getByRole('button', { name: '關閉' }).click();
  await expect(drawer).toHaveCount(0);
});

test('row ⋯ menu offers details, listing and delete', async ({ page }) => {
  await openRecords(page);
  await page.getByTestId('more-student000').click();
  const menu = page.getByRole('menu');
  await expect(menu.getByRole('menuitem')).toHaveText(['詳細資料', '上架到入口', '刪除']);
  await expect(menu.getByRole('menuitem', { name: '刪除' })).toHaveClass(/danger/);
  await menu.getByRole('menuitem', { name: '上架到入口' }).click();
  const dlg = page.getByTestId('listing-dialog');
  await expect(dlg).toBeVisible();
  await expect(dlg.getByLabel('顯示名稱')).toHaveValue('student000');
  await dlg.getByRole('button', { name: '取消' }).click();
  await page.getByTestId('more-gitlab').click();
  await expect(page.getByRole('menuitem', { name: '入口設定' })).toBeVisible();
  await page.mouse.click(5, 5);
  await expect(page.getByRole('menu')).toHaveCount(0);
});

test('前往上游 opens the upstream record', async ({ page }) => {
  await openRecords(page);
  await row(page, 'chain').getByRole('button', { name: '前往上游' }).click();
  await expect(page.getByTestId('record-drawer')).toContainText('alias.example.org');
  await expect(page.getByTestId('record-drawer')).toContainText('CNAME 目標無效');
});

test('batch create by rule: preview then create 20 records', async ({ page }) => {
  await openRecords(page);
  await page.getByRole('button', { name: '批次與匯入匯出 ▾' }).click();
  await page.getByRole('menuitem', { name: '批次新增' }).click();
  const dlg = page.getByTestId('batch-dialog');
  await dlg.getByLabel('名稱範本').fill('vm{n}');
  await dlg.getByLabel('內容範本').fill('10.0.9.{n}');
  await dlg.getByLabel('起始編號').fill('1');
  await dlg.getByLabel('結束編號').fill('20');
  await dlg.getByLabel('名稱補零').selectOption('2');
  await dlg.getByLabel('負責人').fill('課程助教');
  await dlg.getByRole('button', { name: '預覽' }).click();
  await expect(dlg.getByRole('heading', { name: '可建立 20 筆' })).toBeVisible();
  await expect(dlg.locator('tbody tr').first()).toContainText('vm01');
  // Changing a field requires a new preview.
  await dlg.getByLabel('用途').fill('作業系統課');
  await expect(dlg.getByRole('button', { name: /^建立/ })).toBeDisabled();
  await dlg.getByRole('button', { name: '預覽' }).click();
  await dlg.getByRole('button', { name: '建立 20 筆' }).click();
  await expect(toast(page, '已建立 20 筆')).toBeVisible();
  await expect(row(page, 'vm01')).toBeVisible();
  await expect(row(page, 'vm20')).toContainText('作業系統課 · 課程助教');
});

test('CSV import with preview skips duplicates', async ({ page }) => {
  await openRecords(page);
  await page.getByRole('button', { name: '批次與匯入匯出 ▾' }).click();
  await page.getByRole('menuitem', { name: '匯入 CSV' }).click();
  const dlg = page.getByTestId('batch-dialog');
  await dlg.getByLabel('CSV 內容').fill(
    'name,type,content,proxied,ttl,comment,owner,purpose,notes,expires_at,auto_delete,no_scan\n' +
      'lab-a,A,10.0.8.1,false,1,,實驗室,"測試, 機器",,,false,false\n' +
      'lab-b,CNAME,lab-a.example.org,false,1,,,,,,,\n' +
      'vm01,A,10.0.9.1,false,1,,,,,,,\n',
  );
  await dlg.getByRole('button', { name: '預覽' }).click();
  await expect(dlg.getByRole('heading', { name: '可建立 2 筆，1 筆將略過' })).toBeVisible();
  await expect(dlg.locator('tbody tr').nth(2)).toContainText('已有相同的 A record');
  await dlg.getByRole('button', { name: '建立 2 筆' }).click();
  await expect(toast(page, '已建立 2 筆')).toBeVisible();
  await expect(row(page, 'lab-a')).toContainText('測試, 機器 · 實驗室');
});

test('tunnel records show the tunnel and can be managed as a group', async ({ page }) => {
  await openRecords(page);
  const app = row(page, 'tun-app');
  await expect(app.locator('td.c-target')).toContainText('Tunnel home-lab');
  await expect(app.locator('td.c-target')).toContainText('正常 · Proxied');
  await expect(app.locator('td.c-status')).not.toContainText('CNAME 目標不存在');
  await expect(row(page, 'tun-gone').locator('td.c-status')).toContainText('緊急：Tunnel 不存在');
  await page.getByRole('button', { name: '依目標分組' }).click();
  const head = page.locator('tr.group-head', { hasText: 'Tunnel home-lab（2 筆）' });
  const groupBox = head.getByRole('checkbox', { name: '選取指向 Tunnel home-lab 的 2 筆' });
  await row(page, 'tun-app').getByRole('checkbox').check();
  await expect(groupBox).not.toBeChecked();
  expect(await groupBox.evaluate((el: HTMLInputElement) => el.indeterminate)).toBe(true);
  await groupBox.click();
  await expect(groupBox).toBeChecked();
  await expect(page.locator('.bulkbar')).toContainText('已選 2 筆');
  await groupBox.click();
  await expect(page.locator('.bulkbar')).toHaveCount(0);
  await head.getByRole('button', { name: '設定此組管理資訊' }).click();
  const dlg = page.getByTestId('bulk-meta');
  await dlg.getByLabel('修改用途').check();
  await dlg.getByLabel('用途', { exact: true }).fill('經 home-lab tunnel');
  await dlg.getByRole('button', { name: '儲存' }).click();
  await expect(toast(page, '已更新 2 筆')).toBeVisible();
  await page.getByRole('button', { name: '改為清單檢視' }).click();
  await expect(row(page, 'tun-wiki')).toContainText('經 home-lab tunnel');
});

test('batch assign owners from the banner', async ({ page }) => {
  await openRecords(page);
  const banner = page.locator('.banner');
  await expect(banner).toContainText('筆未指派負責人');
  await banner.getByRole('button', { name: '批次指派' }).click();
  const dlg = page.getByTestId('bulk-meta');
  await expect(dlg.getByLabel('修改負責人')).toBeChecked();
  await expect(dlg.getByLabel('負責人', { exact: true })).toBeFocused();
  await expect(dlg.getByLabel('用途', { exact: true })).toBeDisabled();
  await dlg.getByLabel('負責人', { exact: true }).fill('網管組');
  await dlg.getByRole('button', { name: '儲存' }).click();
  await expect(toast(page, /^已更新 \d+ 筆$/)).toBeVisible();
  await expect(banner).toHaveCount(0);
  await expect(row(page, 'grafana')).toContainText('網管組');
});

test('impact section lists owners of dependent CNAMEs', async ({ page }) => {
  await openRecords(page);
  await row(page, 'alias').locator('td.c-type').click();
  const drawer = page.getByTestId('record-drawer');
  const impact = drawer.locator('.dsec', { hasText: '影響範圍' });
  await expect(impact).toContainText('chain');
  await expect(impact).toContainText('網管組');
  await impact.getByRole('button', { name: 'chain' }).click();
  await expect(page.getByTestId('record-drawer')).toContainText('chain.example.org');
});

test('DNS change confirmation: focus on 取消, Enter does not save; then save and wait for the check', async ({ page }) => {
  await openRecords(page);
  await row(page, 'student001').getByRole('button', { name: '編輯' }).click();
  const ed = page.getByTestId('record-editor');
  await ed.getByLabel('IPv4 位址').fill('10.0.3.201');
  await ed.getByRole('button', { name: '儲存' }).click();
  const confirm = page.getByTestId('confirm-dialog');
  await expect(confirm).toContainText('確認 DNS 變更');
  await expect(confirm.locator('del')).toHaveText('10.0.3.101');
  await expect(confirm.locator('strong')).toHaveText('10.0.3.201');
  await expect(confirm.getByRole('button', { name: '取消' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(confirm).toHaveCount(0);
  await expect(ed).toBeVisible();
  await ed.getByRole('button', { name: '儲存' }).click();
  await page.getByTestId('confirm-dialog').getByRole('button', { name: '儲存' }).click();
  await expect(toast(page, '已儲存，連線檢查中')).toBeVisible();
  await expect(row(page, 'student001')).toContainText('10.0.3.201');
  await expect(row(page, 'student001').locator('.checking')).toBeVisible();
  await expect(row(page, 'student001').locator('.dot.pending')).toBeVisible();
  await expect(row(page, 'student001').locator('.checking')).toHaveCount(0, { timeout: 30_000 });
  await expect(row(page, 'student001').locator('.dot.pending')).toHaveCount(0);
});

test('high risk confirmation for a listed record and a rename', async ({ page }) => {
  await openRecords(page);
  await row(page, 'gitlab').getByRole('button', { name: '編輯' }).click();
  const ed = page.getByTestId('record-editor');
  await ed.getByLabel('名稱').fill('gitlab2');
  await ed.getByRole('button', { name: '儲存' }).click();
  const confirm = page.getByTestId('confirm-dialog');
  await expect(confirm).toContainText('確認高風險 DNS 變更');
  await expect(confirm).toContainText('變更類型或名稱等同刪除舊 record 再新增，舊名稱將立即失效。');
  await expect(confirm).toContainText('此 record 已上架到服務入口。');
  await expect(confirm.getByRole('button', { name: '確認變更' })).toHaveClass(/danger/);
  await expect(confirm.getByRole('button', { name: '取消' })).toBeFocused();
  await confirm.getByRole('button', { name: '取消' }).click();
  await ed.getByRole('button', { name: '取消' }).click();
  await expect(row(page, 'gitlab')).toBeVisible();
});

test('meta-only edits save without confirmation; 延長 menu changes the expiry', async ({ page }) => {
  await openRecords(page);
  await row(page, 'student003').getByRole('button', { name: '編輯' }).click();
  const ed = page.getByTestId('record-editor');
  await ed.getByLabel('到期日').fill(taipeiDate(3));
  await ed.getByRole('button', { name: '儲存' }).click();
  await expect(page.getByTestId('confirm-dialog')).toHaveCount(0);
  await expect(toast(page, /^已儲存$/)).toBeVisible();
  const cell = row(page, 'student003').locator('.expiry-cell');
  await expect(cell).toContainText('剩 3 日');
  await cell.getByRole('button', { name: '延長' }).click();
  await page.getByRole('menuitem', { name: /^延長一個月/ }).click();
  await expect(toast(page, /已延長到期日至/)).toBeVisible();
  await expect(cell).not.toContainText('剩 3 日');
  await expect(cell.getByRole('button', { name: '延長' })).toHaveCount(0);
});

test('expiry column hides when no listed record has a date', async ({ page }) => {
  await openRecords(page);
  await expect(page.getByRole('columnheader', { name: /到期/ })).toBeVisible();
  await page.getByLabel('類型').selectOption('TXT');
  await expect(page.getByRole('columnheader', { name: /到期/ })).toHaveCount(0);
  await page.getByRole('button', { name: '清除全部' }).click();
});

test('filters are remembered across reloads', async ({ page }) => {
  await openRecords(page);
  await page.getByLabel('分組').selectOption('student');
  await page.getByLabel('搜尋').fill('00');
  await expect(page.locator('.filter-desc')).toContainText('搜尋「00」、student*');
  await page.reload();
  await expect(page.getByLabel('分組')).toHaveValue('student');
  await expect(page.locator('.filter-desc')).toContainText('搜尋「00」、student*');
  await page.getByRole('button', { name: '清除全部' }).click();
});

test('group by target', async ({ page }) => {
  await openRecords(page);
  await page.getByRole('button', { name: '依目標分組' }).click();
  await expect(page.locator('.group-head', { hasText: '10.0.1.21（2 筆）' })).toBeVisible();
  await expect(page.locator('.group-head', { hasText: /^其他/ })).toBeVisible();
  await page.getByRole('button', { name: '改為清單檢視' }).click();
  await expect(page.locator('.group-head')).toHaveCount(0);
});

test('delete a record and restore it from the audit log', async ({ page }) => {
  await openRecords(page);
  await page.getByTestId('more-student019').click();
  await page.getByRole('menuitem', { name: '刪除' }).click();
  const dlg = page.getByTestId('delete-dialog');
  await expect(dlg).toContainText('A student019.example.org → 10.0.3.119');
  await dlg.getByRole('button', { name: '刪除' }).click();
  await expect(row(page, 'student019')).toHaveCount(0);
  await page.getByRole('link', { name: '操作紀錄' }).click();
  const first = page.locator('.audit-table tbody tr').first();
  await expect(first).toContainText('刪除 record');
  await first.getByRole('button', { name: '重新建立' }).click();
  await expect(page.getByTestId('confirm-dialog')).toContainText('重新建立 A student019.example.org → 10.0.3.119');
  await page.getByTestId('confirm-dialog').getByRole('button', { name: '重新建立' }).click();
  await expect(toast(page, '已還原')).toBeVisible();
  await page.getByRole('link', { name: 'DNS Records' }).click();
  await expect(row(page, 'student019')).toContainText('網管組');
});

test('bulk delete with snapshot, then restore from the snapshot comparison', async ({ page }) => {
  await openRecords(page);
  await row(page, 'vm19').locator('.check-hit').click();
  await row(page, 'vm20').locator('.check-hit').click();
  const bar = page.locator('.bulkbar');
  await expect(bar).toContainText('已選 2 筆');
  await bar.getByRole('button', { name: '刪除' }).click();
  const dlg = page.getByTestId('bulk-delete');
  await expect(dlg).toContainText('刪除前將自動保存快照。');
  await expect(dlg.getByRole('button', { name: '刪除' })).toBeDisabled();
  await dlg.getByText('確認刪除以上 record').click();
  await dlg.getByRole('button', { name: '刪除' }).click();
  await expect(toast(page, '已刪除 2 筆')).toBeVisible();
  await page.getByRole('link', { name: '快照與還原' }).click();
  await page.locator('.snap-table tbody tr').first().getByRole('button', { name: '比較' }).click();
  const diff = page.getByTestId('snapshot-diff');
  await expect(diff).toContainText('已刪除（2）');
  await diff.locator('.diff-row', { hasText: 'vm20' }).getByRole('button', { name: '重新建立' }).click();
  await page.getByTestId('confirm-dialog').getByRole('button', { name: '重新建立' }).click();
  await expect(toast(page, '已還原')).toBeVisible();
  await expect(diff).toContainText('已刪除（1）');
  await page.getByRole('button', { name: '立即建立快照' }).click();
  await expect(toast(page, '已建立快照')).toBeVisible();
});

test('approve a public request', async ({ page, browser }) => {
  const pub = await browser.newPage();
  await pub.goto('/request');
  await pub.getByLabel('姓名').fill('陳同學');
  await pub.getByLabel('聯絡方式').fill('chen@example.com');
  await pub.getByLabel('網址名稱').fill('chen-blog');
  await pub.getByText('其他網址').click();
  await pub.getByLabel('目標網址').fill('chen.github.io');
  await pub.getByLabel('用途').fill('個人部落格');
  await pub.getByRole('button', { name: '送出' }).click();
  await expect(pub.getByTestId('request-status')).toHaveText('審核中');
  const statusURL = pub.url();

  await page.goto('/admin#requests');
  const card = page.locator('.req-card.pending', { hasText: 'chen-blog' });
  await expect(card).toContainText('CNAME → chen.github.io');
  await card.getByRole('button', { name: '核准' }).click();
  const dlg = page.getByTestId('approve-dialog');
  await expect(dlg.getByLabel('負責人')).toHaveValue('陳同學（chen@example.com）');
  await dlg.getByLabel('回覆申請人（選填）').fill('已建立，請設定 GitHub Pages 自訂網域。');
  await dlg.getByRole('button', { name: '核准並建立' }).click();
  await expect(toast(page, '已核准')).toBeVisible();
  await expect(page.locator('.req-card.approved', { hasText: 'chen-blog' })).toBeVisible();

  await pub.goto(statusURL);
  await expect(pub.getByTestId('request-status')).toHaveText('已核准');
  await expect(pub.getByText('已建立，請設定 GitHub Pages 自訂網域。')).toBeVisible();
  await pub.close();
});

test('site settings save and image upload', async ({ page }) => {
  await page.goto('/admin#settings');
  await page.getByLabel('網站名稱').fill('服務入口');
  await page.getByLabel('內容').fill('本週六 02:00 進行機房維護。');
  await page.getByLabel('樣式').selectOption('warn');
  await page.getByRole('button', { name: '儲存設定' }).click();
  await expect(toast(page, '已儲存')).toBeVisible();
  const png = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==',
    'base64',
  );
  await page.getByTestId('upload-logo').setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: png });
  await expect(toast(page, '已上傳')).toBeVisible();
  await expect(page.locator('.media-preview.logo img')).toBeVisible();
  await page.getByTestId('upload-hero').setInputFiles({ name: 'x.png', mimeType: 'image/png', buffer: Buffer.from('<svg/>') });
  await expect(toast(page, '不接受 SVG 格式')).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('網站名稱')).toHaveValue('服務入口');
  await page.locator('.media-field', { hasText: 'Logo' }).getByRole('button', { name: '移除' }).click();
  await expect(page.locator('.media-preview.logo')).toContainText('無');
  await page.getByLabel('網站名稱').fill('Service Portal');
  await page.getByLabel('內容').fill('');
  await page.getByRole('button', { name: '儲存設定' }).click();
  await expect(toast(page, '已儲存')).toBeVisible();
});

test('help tips show above an open dialog on hover and on click', async ({ page }) => {
  await openRecords(page);
  await page.getByRole('button', { name: '新增 Record' }).click();
  const ed = page.getByTestId('record-editor');
  await ed.locator('label[for=ed-type] .tip-icon').hover();
  const tip = page.locator('.tooltip');
  await expect(tip).toBeVisible();
  await expect(tip).toHaveText('A：指向 IPv4 位址，適用於多數自架服務。');
  // The tooltip lives inside the modal dialog so the backdrop does not cover it.
  expect(await tip.evaluate((el) => el.parentElement?.tagName)).toBe('DIALOG');
  await ed.getByLabel('Type').selectOption('CNAME');
  await ed.locator('label[for=ed-type] .tip-icon').click();
  await expect(tip).toContainText('同名不可再有 A、AAAA、CNAME。');
  await ed.getByRole('button', { name: '取消' }).click();
});

test('every admin tab renders without errors and without tag styling', async ({ page }) => {
  for (const tab of ['overview', 'records', 'requests', 'snapshots', 'settings', 'audit']) {
    await page.goto('/admin#' + tab);
    await expect(page.locator('main.page')).not.toBeEmpty();
    await page.waitForLoadState('networkidle');
    // No small labels with a background or border around inline text.
    const tags = await page.evaluate(() =>
      Array.from(document.querySelectorAll('main span, main small, main em')).filter((el) => {
        const s = getComputedStyle(el);
        const hasBg = s.backgroundColor !== 'rgba(0, 0, 0, 0)' && s.backgroundColor !== 'transparent';
        const hasBorder = parseFloat(s.borderTopWidth) > 0 && s.borderTopStyle !== 'none';
        const isDot = el.classList.contains('dot') || el.classList.contains('spin') || el.classList.contains('live') || el.classList.contains('cell');
        return (hasBg || hasBorder) && !isDot && (el.textContent ?? '').trim().length > 0;
      }).length,
    );
    expect(tags, `tag-like elements on #${tab}`).toBe(0);
  }
});
