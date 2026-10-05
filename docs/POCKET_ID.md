# Pocket ID 登录

此分支使用 Pocket ID 作为唯一登录方式。原有用户名密码表单、密码验证、改密、`bootstrap-admin` 和 CLI 账号重置功能均已移除。Pocket ID 不可用或配置缺失时不会回退到本地密码登录。

## 当前部署

在 Pocket ID 的 VoCat OIDC 客户端中配置：

| 项目 | 值 |
| --- | --- |
| Pocket ID / Issuer | `https://auth.bytespark.app` |
| VoCat 地址 | `https://vo.0o.tn` |
| Client ID | `894ea818-24bc-4356-92a8-1658742edebf` |
| 回调 URL | `https://vo.0o.tn/api/auth/oidc/callback` |
| 授权流程 | Authorization Code + PKCE（S256） |
| Scope | `openid profile` |

**必须在 Pocket ID 中将该客户端仅分配给允许管理 VoCat 的用户或组。** 分配给此客户端并成功登录的用户均获得 VoCat 管理权限；VoCat 不根据可变的邮箱或用户名自动绑定旧本地账号。

## 运行配置

以下非敏感变量可以放入部署环境的配置：

```dotenv
VOCAT_OIDC_ISSUER=https://auth.bytespark.app
VOCAT_OIDC_CLIENT_ID=894ea818-24bc-4356-92a8-1658742edebf
VOCAT_OIDC_REDIRECT_URL=https://vo.0o.tn/api/auth/oidc/callback
```

若 Pocket ID 客户端为 confidential client，另通过部署平台的安全配置设置 `VOCAT_OIDC_CLIENT_SECRET`。仅当客户端明确配置为允许不带 secret 的 public client 时才可以省略。不要将 secret 写入 Git、聊天、命令参数或日志。

也可通过 `VOCAT_CONFIG` 指定的 JSON 文件配置 `oidc` 对象，其字段为 `issuer`、`client_id`、`client_secret`、`redirect_url`。环境变量覆盖 JSON。含 secret 的配置文件必须由部署平台或管理员安全保存，限制文件权限。

服务启动时读取 Issuer 的 OIDC discovery；随后需要访问 token 和 JWKS endpoint。云环境及部署主机需允许访问 `auth.bytespark.app`（如果 Pocket ID 在 discovery 中使用其他主机，也需允许对应主机）。HTTPS 的回调地址自动启用 Secure 会话 Cookie；反向代理应将 `/api/auth/oidc/*` 原样转发到 VoCat，并保持统一的公共域名。

## 部署此分支

先构建本分支的前端和服务，再使用上述配置启动：

```bash
cd web
npm ci
npm run build
cd ..
go test ./...
CGO_ENABLED=0 go build -trimpath -o build/vocat ./cmd/vocat
./build/vocat serve
```

不要用尚未包含本分支 OIDC 改动的上游二进制覆盖当前版本。使用 Docker 时先设置以上 `VOCAT_OIDC_*` 变量和 secret，再运行 `docker compose up -d --build`；已有上游镜像不包含本次改动。SQLite 数据卷仍应持久化。

使用 Linux 安装脚本时，无需手动编辑配置文件。缺少 OIDC 配置时，脚本会交互询问 Pocket ID URL / Issuer、Client ID、Callback URL 和 Client Secret。Secret 隐藏输入；confidential client 必须填写，public client 可以留空。输入须为单行，不含空白、引号或反斜杠（Pocket ID 自动生成的 secret 支持此格式）。配置会自动保存到权限为 `0600` 的 `/opt/vocat/data/env`，其他运行配置保留；后续升级沿用已有配置。配置与数据库位于同一数据目录。systemd 和 OpenWrt/procd 服务均读取新路径。

下载脚本后在交互终端运行；同版本从上游切换到本分支时使用 `--force`：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/sdrpsps/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --force 0.3.11
```

重新配置时运行 `sudo bash install.sh --configure-oidc 0.3.11`。此选项会重新询问配置并安装指定版本；回车保留已有值，Secret 输入 `-` 可清除已有值以切换到 public client。无终端的自动化安装也可预先提供 `VOCAT_OIDC_*` 环境变量，脚本会自动保存；secret 应由部署平台安全注入，避免写入命令行或 shell 历史。

安装脚本不生成初始密码。输入取消或配置缺失时，在替换程序前停止。脚本默认使用 `sdrpsps/VoCat` 的 release，并通过 `https://ghfast.top` 加速二进制及 `SHA256SUMS` 下载，失败时自动回退到 GitHub 直连。可用 `VOCAT_REPO` 指定另一个兼容版本来源；显式设置 `VOCAT_GITHUB_PROXY=` 可禁用加速。GitHub API 请求及 token 不经过加速站。

## 升级与会话

升级前备份数据库。如果旧 JSON 配置包含 `admin_username` 或 `admin_password`，请删除这些已不支持的字段。迁移至 schema 26 时会注销所有现有会话并移除 `admins.password_hash` 列；设备、短信和其他业务配置保留。此迁移不能通过直接换回旧程序恢复；回滚旧版本需要恢复升级前的数据库备份。

Pocket ID 的 Issuer 和 `sub` 标识保存在本地会话中，显示名来自已经验证签名的 ID Token。登录使用一次性 state、浏览器绑定 Cookie、nonce 和 PKCE，并检查签名、Issuer、Audience 和过期时间。回调后仅跳转到本站路径，避免外部重定向。

退出登录撤销 VoCat 本地会话。它不会注销 Pocket ID 的全局会话，所以下次使用 Pocket ID 登录可能直接完成。Pocket ID 客户端授权发生变化后，已创建的 VoCat 会话仍可持续到退出或 `VOCAT_SESSION_TTL` 到期；需要即时撤销时应清理相应本地会话或统一缩短 TTL。

## 本地开发

开发环境应使用独立的 Pocket ID 客户端，并注册 loopback 回调。若使用 Vite 的 API 代理，可设置 `http://127.0.0.1:5173/api/auth/oidc/callback`；若使用 Go 嵌入页面，可设置 `http://127.0.0.1:7575/api/auth/oidc/callback`。不要将生产 Client Secret 复制到测试脚本。

HTTP 仅允许 loopback URL，生产环境必须使用 HTTPS。修改回调 URL 后需要同时更新 Pocket ID 客户端及 VoCat 配置。

## 验证

运行 `go test ./...` 和 `cd web && npm test && npm run build`。测试使用本地模拟 OIDC provider，覆盖正常登录、ID Token 校验失败、state/Cookie/过期回调、回调重放、会话身份、CSRF 和退出流程；这些测试不等同于真实 Pocket ID 的浏览器登录验收。

实际部署后，从 VoCat 页面点击“使用 Pocket ID 登录”，确认返回指定页面并获得会话；再验证退出和拒绝未授权的 Pocket ID 用户。若看到登录失败提示，请确认 Client ID、secret、精确回调 URL 和客户端用户/组分配。

本分支发布沿用对应的上游版本号，不因这些修改递增版本。后台只检查 `MengMengCode/VoCat` 的上游发布，发现新版本时提示及时合并上游；合并后保留 Pocket ID 接入，再构建并发布本分支。相同版本号的重构建包安装时使用 `bash install.sh --force`。
