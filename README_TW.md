[English](README.md) | **[繁體中文](README_TW.md)** | [简体中文](README_CN.md)

# sub2api-kiro-enhanced

sub2api-kiro-enhanced 是支援 Kiro 的 AI API 閘道，衍生自 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 與 [nianzs/sub2api 的 Kiro 實作](https://github.com/nianzs/sub2api)。本下游專案獨立維護，結合上游閘道與 Kiro 轉接器，強化路由、協定相容性及快取估算。

## 下游功能重點

變更以固定的 Kiro 上游版本 `5d4ea03887ea219ea77ab447d59efc0c02a816ef` 為比較基準。

- **驗證介面路由與故障切換（擴充）：** 自動模式對 IAM Identity Center 與外部 IdP 權杖依序嘗試 CodeWhisperer、Amazon Q、Kiro Runtime。路由器在每次切換介面及更新權杖後，重建端點／設定檔的請求內容。驗證介面不符可觸發備援；模型、配額及政策錯誤則保留原有分類，不會將每個 403 都視為帳號失效。API 金鑰帳號仍使用 Amazon Q。
- **穩定對話與帳號黏著（沿用）：** 以確定性的對話 ID 與固定帳號選擇，維持多輪對話的連續性。
- **API 相容性（沿用並擴充）：** Kiro 轉接器支援 OpenAI Responses、Chat Completions 與 Anthropic Messages。下游修正 Responses 串流傳輸格式與命名空間，並加入三種協定的緩衝及串流回應快取用量測試。協定轉換不代表與原生 API 完全等價。
- **以原生 credit 校準快取估算（新增）：** 估算器以保守的重用估算取代固定比例模擬，從成功的 Kiro 原生計量及可比較的冷／暖請求觀測學習。sub2api 另行記錄原生 credit 用量。適用範圍與 TTL 見下節。
- **失敗處理與追蹤強化（擴充）：** 估算器排除訊框／CRC 錯誤、事件格式錯誤、失敗或未完成的串流、缺少計量及用戶端寫入失敗的觀測資料。估算器隔離備援嘗試與網頁搜尋生成，避免重複計算完全命中快取的輸入。Kiro 事件追蹤只記錄類型與大小，不擷取提示或推理內容。
- **空回應防護（新增並沿用）：** Anthropic-to-Responses 串流轉接器在完成時若既無輸出項目也無輸出 token，會送出帶有 `upstream_empty_response` 的 `response.failed`。沿用的原生 OpenAI Responses 防護則會將沒有語意輸出及用量的完成事件視為故障切換錯誤，避免向用戶端傳送空白成功回應。
- **建置／發布修正（沿用）：** 保留 Kiro 上游的 Docker 相依套件安裝與法律文件建置內容修正、版本解析指令碼，以及不會自動提交版本檔案的發布流程。

[下游變更紀錄](docs/DOWNSTREAM_CHANGES.md) 詳述實作與回歸測試。

## 架構與適用範圍

Go/Gin/Ent 後端處理閘道路由與協定轉換；Vue/TypeScript 前端提供管理介面。PostgreSQL 儲存持久資料，Redis 支援共用執行狀態。請參閱[後端](backend/)、[前端](frontend/)及[部署檔案](deploy/)。

Kiro 與快取內部機制保留在 sub2api 內。對外 OpenAI／Anthropic 回應結構與欄位集合維持不變。sub2api 不對外輸出估算器的內部中繼資料，並以既有用量欄位回報估算值。

估算器依序對轉換後、模型可見的請求語意計算雜湊，並以帳號／主體、設定檔、端點、模型及推論設定劃分觀測範圍。單純輪替 OAuth 權杖不會改變帳號身分。

| 適用模型 ID | 重用期間（TTL） | 額外範圍 |
| --- | --- | --- |
| `claude-opus-5` | 5 分鐘 | 上述共通範圍 |
| `gpt-5.6-terra` | 30 分鐘 | 另包含原生對話 ID |

這些期間由本實作定義，供應商的快取保留時間並無保證。其他模型不適用。快取 token 數由 sub2api 估算，並非供應商回報的計量值；證據不足時，估算器不會計入節省量。記憶體內的追蹤器有容量上限，重新啟動後會失去校準資料。

## 快速開始與部署

請建置本專案的工作副本，以取得下游功能。使用 [backend/go.mod](backend/go.mod) 指定的 Go 版本（目前為 `1.27.0`）、CI 使用的 Node.js 20、pnpm 9、PostgreSQL 15+ 與 Redis 7+。從儲存庫根目錄執行：

```sh
pnpm --dir frontend install --frozen-lockfile --ignore-scripts
pnpm --dir frontend run build
cd backend
VERSION="$(./scripts/resolve-version.sh)"
go build -tags embed -ldflags="-X main.Version=${VERSION}" -o sub2api ./cmd/server
./sub2api
```

首次從原始碼安裝時，請透過 `http://localhost:8080` 的初始設定精靈設定資料庫、Redis 與管理員帳號。請勿預先建立 `config.yaml`：伺服器偵測到既有設定檔時會略過精靈，而舊版的 `default.admin_email`／`default.admin_password` 欄位不會建立管理員。

容器、服務操作及備份請參閱[部署指南](deploy/README.md)、[Dockerfile](Dockerfile)、[本機目錄版 Compose 檔案](deploy/docker-compose.local.yml)及[具名磁碟區版 Compose 檔案](deploy/docker-compose.yml)。既有映像檔參照、下載指令碼與發布網址仍指向原上游發布者，不能視為下游已發布版本的依據。請建置本工作副本，並將部署設定為使用該建置結果。

## 設定注意事項

- 群組的快取模擬啟用開關控制 Kiro 估算。舊版比例／模式欄位仍接受輸入以維持相容性，但不會決定推算的節省量。閱讀舊部署說明時，請搭配[下游變更紀錄](docs/DOWNSTREAM_CHANGES.md)確認目前的快取語意。
- 使用 Compose 部署時，請設定並安全保存 `JWT_SECRET`、`TOTP_ENCRYPTION_KEY` 與 `POSTGRES_PASSWORD`。保留相同的 JWT／TOTP 金鑰，才能在重新啟動後維持登入狀態，並解密已儲存的雙因素驗證金鑰。
- 部署前請檢查[設定範例](deploy/config.example.yaml)與[環境變數範本](deploy/.env.example)。Docker 建置預設使用公開的相依套件鏡像站；請依環境檢查或覆寫既有建置參數。

## 遠端與來源沿革

| 遠端 | 來源 | 角色 |
| --- | --- | --- |
| `upstream` | `https://github.com/Wei-Shaw/sub2api.git` | 官方專案；每次同步皆須比較 |
| `kiro-upstream` | `https://github.com/nianzs/sub2api.git` | 定期 Kiro 更新的主要來源 |
| `origin` | `https://github.com/SemonCat/sub2api-kiro-enhanced.git` | 本下游；另建分支專案時請使用自己的遠端儲存庫 |

初始公開版本以官方提交 `ab99d56e9626e6cd731592dae8553c9758a0efa2` 為起點，匯入下游快照 `94d954374fee9e3fca624b690ab6fbec8eae82c9` 的檔案差異，其中包含 Kiro 上游 `5d4ea03887ea219ea77ab447d59efc0c02a816ef`。匯入時僅保留官方 Git 祖先關係，不匯入先前的下游歷史。快照 ID 用於記錄來源，不代表本儲存庫必須存在對應提交。公開文件取代內部任務產物。Go 模組名稱維持 `github.com/Wei-Shaw/sub2api` 以確保相容性。

## 定期同步上游

開始前，現有工作應已完成審查及提交，且 `git status --porcelain` 輸出為空。從儲存庫根目錄執行，確認遠端網址，並使用不重複的整合分支名稱（替換 `YYYYMMDD`）。即使主要更新來源是 Kiro，也要擷取並比較兩個上游：

```sh
git remote -v
git fetch upstream
git fetch kiro-upstream
git switch -c sync/kiro-YYYYMMDD
kiro_before=5d4ea03887ea219ea77ab447d59efc0c02a816ef
kiro_after=$(git rev-parse kiro-upstream/main)
git log --oneline "$kiro_before..$kiro_after"
git diff --stat "$kiro_before" "$kiro_after"
git log --oneline HEAD..upstream/main
git diff --stat upstream/main HEAD
```

後續同步時，將 `kiro_before` 改為上次成功整合的 Kiro 版本。同時比較[同步紀錄](docs/UPSTREAM_SYNC.md)中已接受的官方版本至 `upstream/main` 的變更。為保留僅有官方歷史的祖先關係，請匯入 Kiro 的增量檔案差異，而非合併 Kiro 歷史：

```sh
git merge-base --is-ancestor "$kiro_before" "$kiro_after"
```

祖先關係檢查通過且完成差異審查後，才繼續執行。

```sh
kiro_patch=$(mktemp)
git diff --binary --full-index "$kiro_before" "$kiro_after" > "$kiro_patch"
git apply --3way "$kiro_patch"
```

遇到任何錯誤或衝突即停止，依上游意圖與下游行為逐檔解決，不可一律採用某一方。檢查新增檔案是否含私人內容，審閱已暫存及未暫存差異，執行 Kiro／相容性／閘道重點測試、前端回歸測試與前端建置。若涉及資料庫遷移、產生的程式碼或共用路由，須擴大檢查。審查通過後才提交整合結果並更新已接受版本；推送與發布另行處理。Kiro 尚未包含的官方變更須另行審查整合；[同步指南](docs/UPSTREAM_SYNC.md)提供衝突處理與驗證細節。

## 開發與驗證

安裝上述必要工具後，從儲存庫根目錄執行：

```sh
pnpm --dir frontend install --frozen-lockfile --ignore-scripts
make build
make test-unit
make test
(cd backend && go test -tags=unit ./internal/pkg/kiro ./internal/pkg/apicompat ./internal/service ./internal/handler ./internal/handler/admin ./internal/handler/dto -run 'Kiro|AnthropicToResponses|ResponsesNamespace|EmptyCompleted' -count=1)
(cd frontend && pnpm test:run src/composables/__tests__/useKiroOAuth.spec.ts src/i18n/__tests__/forkLocalePreservation.spec.ts src/views/admin/__tests__/groupsKiroCacheMode.spec.ts)
git diff --check
git diff --cached --check
```

`make build` 分別建置前後端；若需要內嵌介面的執行檔，請使用快速開始的建置指令。`make test` 執行後端測試及 `golangci-lint`，再執行前端 lint、型別檢查與關鍵前端測試集；需先安裝 `golangci-lint`。開發時，在不同終端機分別執行 `(cd backend && go run ./cmd/server)` 與 `(cd frontend && pnpm run dev)`。其他檢查請參閱[同步指南](docs/UPSTREAM_SYNC.md)。

## 安全與隱私

妥善保護供應商憑證、API 金鑰、管理員存取權、資料庫備份與加密金鑰。對外開放服務前，請使用 HTTPS 並檢查網址允許清單與反向代理設定；參閱[邊界安全指南](deploy/EDGE_SECURITY.md)。使用前請確認供應商條款與適用要求。

請勿將提示、推理內容、權杖、Cookie、個人資料、內部主機或部署細節放入公開議題、日誌、螢幕擷取畫面及提交。Kiro 事件追蹤只記錄類型與大小，其他日誌仍可能含有敏感資料。分享診斷資料前請先遮蔽敏感內容。公開發布前，請審閱差異並掃描機密資訊。

## 授權與致謝

採用 [GNU Lesser General Public License v3.0 或更新版本](LICENSE)。保留上游著作權標示：Copyright (c) 2026 Wesley Liddick。

感謝 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api)、[nianzs/sub2api](https://github.com/nianzs/sub2api) 及其貢獻者。上游文件與致謝資訊請參閱原專案；本下游獨立維護。
