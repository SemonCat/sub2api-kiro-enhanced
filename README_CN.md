[English](README.md) | [繁體中文](README_TW.md) | **[简体中文](README_CN.md)**

# sub2api-kiro-enhanced

sub2api-kiro-enhanced 是支持 Kiro 的 AI API 网关，源自 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 和 [nianzs/sub2api 的 Kiro 实现](https://github.com/nianzs/sub2api)。本下游项目独立维护，结合上游网关与 Kiro 适配器，增强路由、协议兼容性和缓存估算。

## 下游功能

变更以固定的 Kiro 上游版本 `5d4ea03887ea219ea77ab447d59efc0c02a816ef` 为比较基准。

- **认证接口路由与故障切换（扩展）：** 自动模式对 IAM Identity Center 和外部 IdP 令牌依次尝试 CodeWhisperer、Amazon Q、Kiro Runtime。路由器在每次切换接口及刷新令牌后，重建端点／配置集的请求内容。认证接口不匹配可触发回退；模型、配额和策略错误保留原有分类，不会将每个 403 都视为账号失效。API 密钥账号仍使用 Amazon Q。
- **稳定会话与粘性账号（继承）：** 确定性的会话 ID 和粘性账号选择保持多轮会话的连续性。
- **API 兼容性（继承并扩展）：** Kiro 适配器支持 OpenAI Responses、Chat Completions 和 Anthropic Messages。下游修复 Responses 流式传输格式与命名空间，并添加三种协议的缓冲及流式响应缓存用量测试。协议转换不代表与原生 API 完全等价。
- **通过原生 credit 校准缓存估算（新增）：** 估算器使用保守的复用估算替代固定比例模拟，从成功的 Kiro 原生计量及可比较的冷／热请求观测中学习。sub2api 单独记录原生 credit 用量。适用范围与 TTL 见下节。
- **失败处理与跟踪加固（扩展）：** 估算器排除帧／CRC 错误、事件格式错误、失败或未完成的流、缺少计量及客户端写入失败的观测数据。估算器隔离回退尝试与网页搜索生成，避免重复计算完全命中缓存的输入。Kiro 事件跟踪只记录类型与大小，不摘录提示或推理内容。
- **空响应防护（新增并继承）：** Anthropic-to-Responses 流式适配器在完成时若既无输出项也无输出 token，会发送带有 `upstream_empty_response` 的 `response.failed`。继承的原生 OpenAI Responses 防护会将没有语义输出及用量的完成事件视为故障切换错误，避免向客户端发送空白成功响应。
- **构建／发布修复（继承）：** 保留 Kiro 上游的 Docker 依赖安装与法律文档构建上下文修复、版本解析脚本，以及不会自动提交版本文件的发布流程。

[下游变更记录](docs/DOWNSTREAM_CHANGES.md) 详述实现与回归测试。

## 架构与适用范围

Go/Gin/Ent 后端处理网关路由与协议转换；Vue/TypeScript 前端提供管理界面。PostgreSQL 存储持久数据，Redis 支持共享运行状态。请参阅[后端](backend/)、[前端](frontend/)及[部署文件](deploy/)。

Kiro 与缓存内部机制保留在 sub2api 内。对外 OpenAI／Anthropic 响应结构与字段集合保持不变。sub2api 不对外输出估算器的内部元数据，并通过现有用量字段报告估算值。

估算器按顺序对转换后、模型可见的请求语义计算哈希，并按账号／主体、配置集、端点、模型及推理配置划分观测范围。仅轮换 OAuth 令牌不会改变账号身份。

| 适用模型 ID | 复用窗口（TTL） | 额外范围 |
| --- | --- | --- |
| `claude-opus-5` | 5 分钟 | 上述公共范围 |
| `gpt-5.6-terra` | 30 分钟 | 另包含原生会话 ID |

这些窗口由本实现定义，供应商的缓存保留时间没有保证。其他模型不适用。缓存 token 数由 sub2api 估算，并非供应商报告的计量值；证据不足时，估算器不会计入节省量。内存中的跟踪器有容量上限，重启后会丢失校准数据。

## 快速开始与部署

请构建本项目的工作副本，以获得下游功能。使用 [backend/go.mod](backend/go.mod) 指定的 Go 版本（当前为 `1.27.0`）、CI 使用的 Node.js 20、pnpm 9、PostgreSQL 15+ 和 Redis 7+。从仓库根目录执行：

```sh
pnpm --dir frontend install --frozen-lockfile --ignore-scripts
pnpm --dir frontend run build
cd backend
VERSION="$(./scripts/resolve-version.sh)"
go build -tags embed -ldflags="-X main.Version=${VERSION}" -o sub2api ./cmd/server
./sub2api
```

首次从源码安装时，请通过 `http://localhost:8080` 的初始设置向导配置数据库、Redis 和管理员账号。请勿预先创建 `config.yaml`：服务器检测到已有配置文件时会跳过向导，而旧版 `default.admin_email`／`default.admin_password` 字段不会创建管理员。

容器、服务操作及备份请参阅[部署指南](deploy/README.md)、[Dockerfile](Dockerfile)、[本地目录版 Compose 文件](deploy/docker-compose.local.yml)及[命名卷版 Compose 文件](deploy/docker-compose.yml)。现有镜像引用、下载脚本与发布链接仍指向原上游发布者，不能作为下游已发布版本的依据。请构建本工作副本，并将部署配置为使用该构建结果。

## 配置注意事项

- 分组的缓存模拟启用开关控制 Kiro 估算。旧版比例／模式字段仍接受输入以保持兼容性，但不会决定推算的节省量。阅读旧部署说明时，请结合[下游变更记录](docs/DOWNSTREAM_CHANGES.md)确认当前缓存语义。
- 使用 Compose 部署时，请设置并安全保存 `JWT_SECRET`、`TOTP_ENCRYPTION_KEY` 和 `POSTGRES_PASSWORD`。保留相同的 JWT／TOTP 密钥，才能在重启后维持登录状态，并解密已存储的双因素认证密钥。
- 部署前请检查[配置示例](deploy/config.example.yaml)与[环境变量模板](deploy/.env.example)。Docker 构建默认使用公开依赖镜像站；请根据环境检查或覆盖现有构建参数。

## 远程仓库与来源沿革

| 远程仓库 | 来源 | 角色 |
| --- | --- | --- |
| `upstream` | `https://github.com/Wei-Shaw/sub2api.git` | 官方项目；每次同步均须比较 |
| `kiro-upstream` | `https://github.com/nianzs/sub2api.git` | 定期 Kiro 更新的主要来源 |
| `origin` | `https://github.com/SemonCat/sub2api-kiro-enhanced.git` | 本下游；另建分叉项目时请使用自己的目标仓库 |

初始公开版本以官方提交 `ab99d56e9626e6cd731592dae8553c9758a0efa2` 为起点，导入下游快照 `94d954374fee9e3fca624b690ab6fbec8eae82c9` 的文件差异，其中包含 Kiro 上游 `5d4ea03887ea219ea77ab447d59efc0c02a816ef`。导入时仅保留官方 Git 祖先关系，不导入此前的下游历史。快照 ID 用于记录来源，不代表本仓库必须存在对应提交。公开文档替代内部任务产物。Go 模块名称保持 `github.com/Wei-Shaw/sub2api` 以确保兼容性。

## 定期同步上游

开始前，现有工作应已完成审查及提交，且 `git status --porcelain` 输出为空。从仓库根目录执行，确认远程仓库地址，并使用唯一的集成分支名称（替换 `YYYYMMDD`）。即使主要更新来源是 Kiro，也要获取并比较两个上游：

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

后续同步时，将 `kiro_before` 改为上次成功集成的 Kiro 版本。同时比较[同步记录](docs/UPSTREAM_SYNC.md)中已接受的官方版本至 `upstream/main` 的变更。为保留仅有官方历史的祖先关系，请导入 Kiro 的增量文件差异，而非合并 Kiro 历史：

```sh
git merge-base --is-ancestor "$kiro_before" "$kiro_after"
```

祖先关系检查通过且完成差异审查后，才继续执行。

```sh
kiro_patch=$(mktemp)
git diff --binary --full-index "$kiro_before" "$kiro_after" > "$kiro_patch"
git apply --3way "$kiro_patch"
```

遇到任何错误或冲突即停止，按上游意图与下游行为逐文件解决，不可一律采用某一方。检查新增文件是否包含私人内容，审阅已暂存及未暂存差异，运行 Kiro／兼容性／网关重点测试、前端回归测试与前端构建。涉及数据库迁移、生成的代码或共享路由时，须扩大检查。审查通过后才提交集成结果并更新已接受版本；推送与发布单独处理。Kiro 尚未包含的官方变更须单独审查集成；[同步指南](docs/UPSTREAM_SYNC.md)提供冲突处理与验证细节。

## 开发与验证

安装上述必要工具后，从仓库根目录执行：

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

`make build` 分别构建前后端；如需内嵌界面的可执行文件，请使用快速开始的构建命令。`make test` 运行后端测试及 `golangci-lint`，再运行前端 lint、类型检查与关键前端测试集；需先安装 `golangci-lint`。开发时，在不同终端分别运行 `(cd backend && go run ./cmd/server)` 与 `(cd frontend && pnpm run dev)`。其他检查请参阅[同步指南](docs/UPSTREAM_SYNC.md)。

## 安全与隐私

妥善保护供应商凭据、API 密钥、管理员访问权限、数据库备份和加密密钥。对外开放服务前，请使用 HTTPS 并检查 URL 允许列表与反向代理配置；参阅[边界安全指南](deploy/EDGE_SECURITY.md)。使用前请确认供应商条款与适用要求。

请勿将提示、推理内容、令牌、Cookie、个人数据、内部主机或部署细节放入公开议题、日志、截图及提交。Kiro 事件跟踪只记录类型与大小，其他日志仍可能包含敏感数据。分享诊断材料前请先脱敏。公开发布前，请审阅差异并扫描机密信息。

## 许可与致谢

采用 [GNU Lesser General Public License v3.0 或更高版本](LICENSE)。保留上游版权声明：Copyright (c) 2026 Wesley Liddick。

感谢 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api)、[nianzs/sub2api](https://github.com/nianzs/sub2api) 及其贡献者。上游文档与致谢信息请参阅原项目；本下游独立维护。
