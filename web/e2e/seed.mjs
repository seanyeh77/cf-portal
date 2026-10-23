// Logs in and lists a few services on the portal. Used by the Playwright
// global setup and for manual runs against scripts/dev-server.sh.
export const BASE = process.env.BASE_URL || 'http://127.0.0.1:18080';
export const MOCK = process.env.MOCK_URL || 'http://127.0.0.1:18081';
export const PASSWORD = process.env.ADMIN_PASSWORD || 'test-password-123';

export async function login(request) {
  const res = await request.post(BASE + '/login', { form: { username: 'admin', password: PASSWORD }, maxRedirects: 0 });
  if (res.status() !== 303) throw new Error('login failed: ' + res.status());
}

export async function seed(request) {
  await login(request);
  const h = { 'X-Requested-With': 'cf-portal' };
  const data = await (await request.get(BASE + '/api/records')).json();
  const id = (n) => data.records.find((r) => r.name === n + '.example.org')?.id;
  const listings = [
    ['gitlab', { title: 'GitLab', category: '開發工具', description: '程式碼代管與 CI。', tags: ['需帳號', '成員限定'], featured: true, icon: 'auto', url: MOCK + '/svc/gitlab/' }],
    ['grafana', { title: 'Grafana', category: '監控', description: '主機與服務監控儀表板。', tags: ['需 VPN'], featured: true, icon: 'si:grafana', url: MOCK + '/svc/grafana/' }],
    ['jupyter', { title: 'JupyterHub', category: '分析', description: '共用 Notebook 環境，每位成員一個帳號。', tags: ['內部使用'], icon: 'none', url: MOCK + '/svc/down/' }],
    ['git', { title: 'Gitea 鏡像', category: '開發工具', description: '外部套件鏡像。', tags: [], icon: 'none', url: MOCK + '/svc/git/' }],
    ['wg', { title: 'WireGuard', category: '網路', description: 'VPN 設定檔下載與說明。', tags: ['成員限定'], icon: 'si:wireguard', url: MOCK + '/svc/wg/' }],
  ];
  for (const [name, body] of listings) {
    const res = await request.put(`${BASE}/api/listings/${id(name)}`, { data: body, headers: h });
    if (!res.ok()) throw new Error(`listing ${name}: ${res.status()} ${await res.text()}`);
  }
}

// node e2e/seed.mjs — seed a running dev server.
if (process.argv[1] && import.meta.url === new URL('file://' + process.argv[1]).href) {
  const { request } = await import('@playwright/test');
  const r = await request.newContext();
  await seed(r);
  console.log('seeded');
}
