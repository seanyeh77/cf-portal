# Cloudflare 服務入口與 DNS 管理後台

管理 Cloudflare 上的一個 zone（例如 `example.org`）的服務入口與 DNS 後台，適用於任何社群、實驗室或團隊：

- 服務入口 `/`：使用者查看目前有哪些服務、網址與是否正常。
- 管理後台 `/admin`：讀寫 Cloudflare DNS record，記錄負責人、用途與到期日，找出有問題的 record，修改前顯示差異，所有變更可還原。
- 網域申請 `/request`：使用者申請子網域，管理者審核後自動建立。

單一 Go 執行檔（前端以 `embed` 內嵌）＋ SQLite，部署為一個 container 與一個 volume。

---

## 1. 快速開始

```sh
cp .env.example .env
# 1) 填入 CF_API_TOKEN（見第 2 節）
# 2) 產生密碼雜湊，貼到 ADMIN_PASSWORD_HASH
docker compose build
docker compose run --rm portal hashpw
# 3) 產生 SECRET_KEY，貼到 .env
openssl rand -base64 48
# 4) 啟動
docker compose up -d --build
docker compose ps        # STATUS 應為 healthy
```

服務監聽 container 的 8080（compose 對外開放 8080）。以瀏覽器開啟 `https://<主機>/`；管理後台為 `/admin`。

## 2. 建立 Cloudflare API token

Cloudflare 控制台 → My Profile → API Tokens → Create Token → Custom token：

| 權限 | 等級 |
|---|---|
| Zone · DNS | Edit |
| Zone · Zone | Read |

Zone Resources 選 **Include · Specific zone · example.org**，不要授權其他 zone。可加上 Client IP Address Filtering 限定這台主機的對外 IP。
若想省去啟動時以名稱查詢 zone id，可在 Overview 頁右下角複製 Zone ID 填入 `CF_ZONE_ID`。

## 3. 密碼雜湊與 SECRET_KEY

- `portal hashpw` 會要求輸入兩次密碼（至少 12 字元），輸出 `argon2id:<m>:<t>:<p>:<salt>:<hash>`。格式刻意不含 `$`，可直接放進 `.env`，不會被 docker compose 的變數插值破壞。
- `SECRET_KEY` 用來簽 session cookie，至少 32 字元。更換後所有人需重新登入。
- 帳號只有一組（`ADMIN_USERNAME`，預設 `admin`），由幹部共用並保存在密碼管理器。同一 IP 連續失敗 5 次會鎖 15 分鐘。

## 4. 存取限制：允許清單與 wg-easy

除了 `/healthz`，所有路徑都會檢查來源 IP：

- `ALLOWED_CIDRS`：服務入口、申請頁等公開頁面。
- `ADMIN_ALLOWED_CIDRS`：`/admin`、`/api/*`、`/login`、`/logout`。
- 不在清單內回 403「此頁面只開放內部網路或 VPN 存取。」。IPv4-mapped IPv6（`::ffff:10.0.0.1`）會先轉回 IPv4 再比對。

**wg-easy（WireGuard）**：wg-easy 會做 NAT，使用者經 VPN 連入時，本系統看到的來源 IP 是 **wg-easy 那台 VM 的內網 IP**，不是 WireGuard 位址（例如 10.8.0.x）。因此：

- 只要 wg-easy VM 的內網 IP 在 `ALLOWED_CIDRS` 內，所有 VPN 使用者都能開啟入口。
- 若只想讓少數人進後台，`ADMIN_ALLOWED_CIDRS` 不能用 VPN 區分個人（大家都是同一個 NAT IP），只能靠帳號密碼；或另設不經 NAT 的管理網段。

**Docker 注意事項**：預設清單刻意**不含** `172.16.0.0/12`。Docker bridge gateway 位於此範圍；經 Docker userland proxy 轉送的連線，來源會變成 gateway IP，若允許此範圍等於繞過限制。
在主機本機 `curl localhost:8080` 會得到 403，這是正常的：本機連線經 docker-proxy，來源是 bridge gateway。從其他內網主機連入時，Docker 以 iptables DNAT 轉送，會保留真實來源 IP。

## 5. Reverse proxy 與 `TRUSTED_PROXIES`

建議在前方放 Caddy 或 nginx 處理 HTTPS。只有連線來源屬於 `TRUSTED_PROXIES` 時，才會讀取 `X-Forwarded-For`（取最右邊、不在信任清單內的 IP）與 `X-Forwarded-Proto`。

- reverse proxy 與本系統在**同一台主機、同一個 compose**：把 proxy 加進 compose，讓 proxy 以 container 網路連到 `portal:8080`，並把 `TRUSTED_PROXIES` 設成 proxy container 的固定 IP（建議以自訂 network 指定 `ipv4_address`）。
- reverse proxy 在**另一台主機**：`TRUSTED_PROXIES` 設為那台主機的 IP。
- proxy 必須**覆寫**而非附加使用者送來的 `X-Forwarded-For`，或至少附加真實來源 IP（Caddy 與 nginx `$proxy_add_x_forwarded_for` 皆會附加在最右邊）。

Caddy 範例：

```caddy
cf-portal.internal.example {
    tls internal
    reverse_proxy portal:8080
}
```

### 不可使用 Cloudflare Tunnel 或 Proxied record 發布本系統

本系統的安全模型建立在「來源 IP 為內網或 VPN」上。若以 Cloudflare Tunnel 或開啟 Proxy 的 record 對外發布：

- 所有連線的來源都會變成 tunnel connector 或 Cloudflare 的 IP，允許清單失去意義；
- 管理後台與 Cloudflare API token 的權限會暴露在網際網路上。

請只在內網或 VPN 內提供服務；若需要網域名稱，請建立**未啟用 Proxy**、指向內網 IP 的 A record。

## 6. HTTPS 與 `COOKIE_SECURE`

- 預設 `COOKIE_SECURE=true`：session cookie 只在 HTTPS 傳送。此時若以 HTTP 開啟登入頁，會顯示「目前為 HTTP 連線，無法保存登入狀態。請改用 HTTPS，或設定 COOKIE_SECURE=false。」，且拒絕登入。
- 經 reverse proxy 終止 TLS 時，proxy 必須送出 `X-Forwarded-Proto: https`，且 proxy 的 IP 必須在 `TRUSTED_PROXIES` 內。
- 只在純內網測試、確定不經 HTTPS 時才設 `COOKIE_SECURE=false`。

## 7. 自簽憑證

入口的狀態檢查與「自動取得圖示」預設會驗證服務的憑證。若內網服務使用自簽憑證，設定 `CHECK_VERIFY_TLS=false`，否則這些服務會顯示為「無回應」。
（record 連線掃描在檢查 Proxied record 時本來就不驗證憑證，只看 HTTP 狀態碼。）

## 8. 備份

所有狀態都在 `/data`（named volume `portal-data`）：

| 路徑 | 內容 |
|---|---|
| `portal.db`（含 `-wal`、`-shm`） | 上架設定、管理資訊、申請、稽核紀錄、網站設定 |
| `icons/`、`media/` | 服務圖示、Logo、頁首背景圖 |
| `snapshots/` | 每日自動與手動快照（JSON，可下載為 BIND zone 檔） |

DNS record 本身存在 Cloudflare，快照可作為額外備份。備份範例：

```sh
docker compose stop portal
docker run --rm -v cf-portal_portal-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/portal-data-$(date +%F).tgz -C /data .
docker compose start portal
```

（volume 名稱為 `<compose 專案名>_portal-data`，可用 `docker volume ls` 確認。）

## 9. 環境變數

| 變數 | 預設 | 說明 |
|---|---|---|
| `CF_API_TOKEN` | 必填 | Cloudflare API token（Zone.DNS:Edit、Zone.Zone:Read，限定此 zone） |
| `CF_ZONE_NAME` | 必填 | 管理的 zone，例如 `example.org` |
| `CF_ZONE_ID` | 空 | 選填；有設就不用名稱查 zone id |
| `CF_API_BASE` | `https://api.cloudflare.com/client/v4` | 測試時改指向模擬伺服器 |
| `ADMIN_USERNAME` | `admin` | 管理者帳號 |
| `ADMIN_PASSWORD_HASH` | 必填 | `portal hashpw` 產生 |
| `SECRET_KEY` | 必填 | 至少 32 字元，簽 session 用 |
| `SESSION_HOURS` | `12` | 登入有效時數 |
| `COOKIE_SECURE` | `true` | 只在 HTTPS 傳送 cookie |
| `ALLOWED_CIDRS` | `127.0.0.0/8,::1/128,10.0.0.0/8,192.168.0.0/16,fc00::/7` | 入口與公開頁允許的來源 |
| `ADMIN_ALLOWED_CIDRS` | 同 `ALLOWED_CIDRS` | `/admin`、`/api`、`/login`、`/logout` 允許的來源 |
| `TRUSTED_PROXIES` | `127.0.0.1` | 只信任這些 IP（或 CIDR）送來的 `X-Forwarded-For` / `X-Forwarded-Proto` |
| `DB_PATH` | `/data/portal.db` | SQLite 位置；圖示、媒體、快照放在同目錄下的 `icons/`、`media/`、`snapshots/` |
| `PORTAL_TITLE` | `Service Portal` | 初始網站名稱，之後在後台修改 |
| `CHECK_INTERVAL` | `60` | 上架服務狀態檢查間隔（秒） |
| `CHECK_TIMEOUT` | `8` | 每次 HTTP 檢查逾時（秒） |
| `CHECK_VERIFY_TLS` | `true` | 檢查與抓圖示時是否驗證憑證 |
| `SIMPLE_ICONS_BASE` | `https://cdn.jsdelivr.net/npm/simple-icons@latest/icons` | Simple Icons SVG 來源（伺服器端下載一次後快取） |
| `RECORD_SCAN_INTERVAL` | `21600` | 所有 record 連線掃描間隔（秒） |
| `SCAN_TIMEOUT` | `2.5` | 每個 TCP 連線逾時（秒） |
| `SCAN_DNS_CANARY` | `cloudflare.com` | 確認 container 能查外部 DNS 的網域 |
| `MAINT_INTERVAL` | `3600` | 例行作業（每日快照、到期刪除）間隔（秒） |
| `SNAPSHOT_KEEP` | `30` | 保留幾份自動快照（手動快照不會自動刪除） |
| `TIMEZONE` | `Asia/Taipei` | 判斷「今天」與到期日用 |
| `LISTEN_ADDR` | `:8080` | 監聽位址 |

啟動時會檢查必填變數、CIDR 格式與時區，錯誤時印出所有問題並結束。

## 10. 入口頁快捷鍵（選用）

所有功能都能只用滑鼠完成；以下快捷鍵為 nvim 風格的選用功能，觸控裝置與窄螢幕不顯示提示。

| 按鍵 | 動作 |
|---|---|
| `j` `k` `h` `l`、方向鍵 | 依實際排版在卡片間移動 |
| `gg` / `G` | 第一個 / 最後一個 |
| `n` / `N` | 下一個 / 上一個 |
| `gt` / `gT` | 下一個 / 上一個分類 |
| `Ctrl-d` / `Ctrl-u` | 往下 / 往上半頁 |
| `o`、`Enter` | 新分頁開啟 |
| `yy` | 複製網址 |
| `/` | 聚焦搜尋；`Enter` 選第一筆，`Esc` 清除 |
| `:`、`Ctrl-k` | 命令列：`open 名稱`（或直接輸入名稱）、`cat 分類`、`set bg=dark\|light\|auto`、`noh`、`help`、`q` |
| `?` | 快捷鍵說明 |
| `Esc` | 取消選取；再按一次清除篩選 |

## 11. 開發與測試

需要 Go 1.26 以上（modernc.org/sqlite 與 x/crypto 的最低要求）、Node 20 以上。

```sh
# Go 單元測試與 API 端到端測試（內含 Cloudflare 模擬伺服器）
go test ./...

# 前端
cd web
npm ci
npm run build                     # 輸出至 internal/webui/dist/app，供 Go embed
npx playwright install chromium
npm run test:e2e                  # 自動啟動 scripts/dev-server.sh

# 手動開發：模擬 Cloudflare + 本系統（http://127.0.0.1:18080，帳號 admin / test-password-123）
scripts/dev-server.sh
(cd web && node e2e/seed.mjs)   # 選用：上架幾個示範服務
```

目錄：

```
cmd/portal              進入點：serve、hashpw、health
internal/config         環境變數
internal/httpapi        handlers、允許清單、session、CSRF
internal/cloudflare     Cloudflare API client
internal/dnsrec         名稱正規化、內容驗證、衝突檢查、命名規則
internal/analysis       問題分析規則
internal/scanner        record 連線掃描、例行作業
internal/health         上架服務狀態檢查
internal/assets         圖片檔頭驗證、圖示取得
internal/snapshots      快照、差異比較、BIND 匯出
internal/store          SQLite schema、migration、查詢
internal/webui          內嵌的前端
web/                    React + Vite（src/portal、src/admin、src/shared、e2e）
testdata/mockcf         Cloudflare 模擬伺服器
```
