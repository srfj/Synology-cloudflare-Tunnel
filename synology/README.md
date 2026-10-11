# 群晖（Synology）DSM 套件：Cloudflare Tunnel

把本仓库的 `cloudflared` 及配套管理助手 `cfdctl` 打包成群晖 DSM 6.2.4 原生套件（`.spk`），
无需 SSH、无需命令行，即可在「套件中心」安装，并在 DSM 桌面用图形界面查看隧道运行状态与日志。

## 功能特性

- **安装向导必填 Tunnel Token**：安装时弹出向导填写 Token，校验非空，直接写入数据目录。
- **安装后自动运行**：套件启动时若已配置 Token 会自动拉起隧道；NAS 重启后同样自动恢复。
- **免登录只读状态页**：点击桌面图标即可查看「运行状态 + 运行日志」，不依赖 DSM 账号登录。
- **Token 隐藏防泄漏**：
  - 通过 `TUNNEL_TOKEN_FILE` 把 Token 文件路径传给 cloudflared，Token 不出现在 `ps` 进程列表和进程环境变量里；
  - 状态接口只返回固定掩码，绝不返回明文；
  - 日志接口回传前会把日志中出现的 Token 替换成掩码。
- **Cloudflare 官方图标**：桌面图标与套件中心图标均使用官方云标。
- **地址栏不带自定义端口**：桌面图标打开 DSM 原生路径 `/webman/3rdparty/cloudflared/index.html`，
  管理服务实际监听的 8321 端口由页面在后台静默调用，不出现在地址栏。
- **连接诊断与加速**：状态页展示实际传输协议与边缘连接，可切换自动 / QUIC / HTTP/2，以及 IPv4/IPv6、HA 连接数、禁用 QUIC PMTU。

## 目录结构

```
synology/
├── build-spk.sh              # 一键构建脚本，产出 .spk
├── cfdctl/
│   ├── main.go               # 管理助手：托管隧道进程 + 提供状态/日志接口
│   ├── settings.go           # 加速设置（accel.json）与启动参数
│   ├── diagnose.go           # 从日志解析协议、边缘连接与回退提示
│   └── web/index.html        # 状态页（运行状态、诊断、加速、运行日志）
├── gen-icons/main.go         # 生成官方 Cloudflare 图标（桌面 + 套件中心）
├── scripts/
│   ├── postinst              # 安装后：写入向导 Token、生成说明文件
│   └── start-stop-status     # 套件启停/状态脚本（启动 cfdctl）
├── ui/
│   └── config                # DSM 桌面图标配置（url 指向 webman 路径）
└── wizard/
    ├── install_uifile        # 安装向导（英文）
    └── install_uifile_chs    # 安装向导（简体中文）
```

## 构建

需要 Go 工具链、`tar`、`gzip`、`md5sum`。

```bash
# 默认构建 x86_64（amd64）
./synology/build-spk.sh

# 构建 arm64（如 DS218play / RTD1296 等）
GOARCH_TARGET=arm64 OUT_NAME=armv8 SPK_ARCH="rtd1296 armv8" \
  ./synology/build-spk.sh
```

产物位于仓库根目录：`cloudflared-<版本>-dsm6.2.4-<架构>.spk`。
脚本会静态交叉编译（`CGO_ENABLED=0`），以兼容 DSM 6.2.4 的旧内核。

常用环境变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PKG_VERSION` | `2026.9.29` | 显示版本 |
| `SPK_VERSION` | `${PKG_VERSION}-8` | 套件版本，末尾修订号递增可让 DSM 识别为升级 |
| `GOARCH_TARGET` | `amd64` | Go 目标架构 |
| `SPK_ARCH` | x86_64 机型列表 | INFO 中的 `arch` |
| `OUT_NAME` | `x86_64` | 产物文件名中的架构标识 |
| `ADMIN_PORT` | `5000` | 用于拼接 Package Center「打开」链接的 DSM Web 端口，非 5000 时请修改 |
| `ADMIN_URL` | `webman/3rdparty/cloudflared/index.html` | 管理页在 DSM Web 根下的相对路径 |

## 安装

1. 打开 DSM「套件中心」→「手动安装」，选择构建出的 `.spk`。
2. 安装向导中粘贴隧道 Token（**必填**）。
   Token 获取：Cloudflare Zero Trust 控制台 → **Networks → Tunnels** → 选择/创建隧道 → 复制 Token。
3. 安装完成后隧道会自动启动；点桌面/主菜单中的 **Cloudflare Tunnel** 图标查看状态与日志。

状态页地址：`http://<NAS-IP>:5000/webman/3rdparty/cloudflared/index.html`
（直接点 DSM 图标即可，地址栏为普通 DSM 网址，不带自定义端口。）

## 配置与管理

- **更换 Token**：状态页为只读，不含 Token 输入。可通过 SSH 更新后重启套件：

  ```bash
  printf '%s' '<新的TUNNEL-TOKEN>' > /var/packages/cloudflared/var/tunnel-token
  synopkg restart cloudflared
  ```

  或重新安装套件并在向导中填入新 Token。
- **连接加速**：状态页可选自动、低延迟（QUIC）、稳定优先（HTTP/2）。网页元素慢时先看诊断：QUIC 回退或 UDP 7844 被拦则改 HTTP/2；已是 QUIC 仍慢可强制 IPv4 或禁用 PMTU。设置保存在 `var/accel.json`。
- **使用 config.yml**：把配置文件放到 `/var/packages/cloudflared/var/config.yml`，
  管理助手在无 Token 时会自动改用 `cloudflared tunnel --config ... run`。

### 关键路径

| 路径 | 说明 |
|------|------|
| `/var/packages/cloudflared/target/bin/cloudflared` | 隧道主程序 |
| `/var/packages/cloudflared/target/bin/cfdctl` | 管理助手 |
| `/var/packages/cloudflared/var/tunnel-token` | 隧道 Token（权限 600） |
| `/var/packages/cloudflared/var/config.yml` | 可选配置文件 |
| `/var/packages/cloudflared/var/accel.json` | 连接加速设置（协议 / IP / HA / PMTU） |
| `/var/packages/cloudflared/var/cloudflared.log` | 隧道运行日志（状态页展示） |
| `/var/packages/cloudflared/var/README.txt` | 安装后生成的说明文件 |

## 兼容性

- DSM **6.2.4** 及以上（`os_min_ver="6.2-00000"`），静态编译、无外部依赖。
- 默认覆盖常见 x86_64 机型；ARM 机型请按上文用 `SPK_ARCH` / `GOARCH_TARGET` 重新构建。

## 故障排查

- **状态页打不开**：确认套件处于「运行中」，且 `cfdctl` 已在 8321 端口监听
  （`/var/packages/cloudflared/var/cfdctl.log`）。
- **隧道未运行**：在状态页查看日志；多为 Token 无效或未填写。
- **公开域名网页很慢**：看状态页诊断。回退到 HTTP/2 或提示 UDP 被拦时选「稳定优先」；仍走 QUIC 时试强制 IPv4 或禁用 PMTU。Zero Trust 公开主机名的源站请填 NAS 内网地址（如 `http://127.0.0.1:5000`）。
- **安装向导未弹出 Token 输入**：极少数 DSM 版本对 `WIZARD_UIFILES` 支持不一致，
  改用上面的 SSH 方式写入 Token 后 `synopkg restart cloudflared` 即可。
