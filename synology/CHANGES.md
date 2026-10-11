# 群晖套件改造记录（修改经验）

本文记录把 cloudflared 封装为群晖 DSM 6.2.4 套件（`.spk`）过程中的迭代与踩坑经验，供后续维护参考。
面向使用者的安装/配置说明见 [README.md](README.md)。

## 版本迭代

| 套件版本 | 主要变化 |
| --- | --- |
| `-3` | 首个可安装版本：`cloudflared` + `cfdctl`、静态交叉编译、桌面图标、通过 DSM 原生地址打开管理页 |
| `-5` | 图标改为 Cloudflare 官方云标；新增套件中心图标 `PACKAGE_ICON.PNG` / `PACKAGE_ICON_256.PNG` |
| `-6` | 安装向导必填 Tunnel Token；安装后自动运行；去掉登录鉴权；Token 隐藏（`TUNNEL_TOKEN_FILE` + 掩码） |
| `-7` | 状态页精简为「运行状态 + 运行日志」，移除启停按钮与 Token 输入栏 |
| `-8` | 状态页增加连接诊断与加速设置：可切换 auto/QUIC/HTTP2、IPv4/IPv6、HA 连接数、禁用 QUIC PMTU |

## 经验要点

### 1. 图标
- DSM 桌面图标来自 `dsmuidir`（`ui/images/icon_{16,24,32,48,64,72,256}.png`）。
- 套件中心图标需要 **SPK 根目录**（不是 `package.tgz` 内）的 `PACKAGE_ICON.PNG`(72×72) 与 `PACKAGE_ICON_256.PNG`(256×256)。
- 套件中心图标在**安装时**读取；升级后若仍显示旧图标，刷新 Package Center 页面即可。
- 图标由 `synology/gen-icons` 用纯标准库生成：内置官方 Cloudflare 路径 + 自实现 SVG 光栅化（贝塞尔细分、非零环绕填充、超采样抗锯齿），避免引入第三方依赖。

### 2. 打开地址不带自定义端口
- 管理服务实际监听 `8321`，但 INFO 里把 `adminport` 设为 DSM 的 `5000`、`adminurl` 设为 `webman/3rdparty/cloudflared/index.html`，
  点击图标即走 DSM 原生路径，地址栏不暴露自定义端口。
- 页面内 API 地址按 `location.hostname` 拼到同主机 `8321`，并对该端口放行 CORS（`Access-Control-Allow-Origin` + `Authorization`）。

### 3. 安装向导（Token 必填）
- 机制：SPK 根目录 `WIZARD_UIFILES/install_uifile`（英文）与 `install_uifile_chs`（简体中文），内容是 JSON 数组；
  用 `validator.allowBlank=false` 实现必填。
- 向导值以环境变量形式传入安装脚本（`scripts/postinst`），键名即 JSON 里的 `key`（此处为 `WIZARD_TOKEN`）。
- 升级流程默认**不弹**向导（`silent_upgrade="yes"`），因此写入前判断是否有值，避免升级把已有 Token 清空。
- 兼容性：个别 DSM 版本对 `WIZARD_UIFILES` 支持不一致；因此保留「写 `var/tunnel-token` 后 `synopkg restart`」的兜底方式。

### 4. Token 安全
- 不要用 `cloudflared tunnel run --token <TOKEN>`：Token 会出现在 `ps` 进程列表。
  改用 `TUNNEL_TOKEN_FILE=<文件>` 指向 `var/tunnel-token`（权限 600）。
- 状态接口只返回固定掩码，绝不回传明文；日志接口在返回前把日志中出现的 Token 替换为掩码。
- 换 Token 接口设计为「只写不读」。

### 5. 静态编译
- `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -tags "osusergo netgo"`，兼容 DSM 6.2.4 的旧内核与 libc。
- `cfdctl` 只用标准库，避免在 NAS 上引入额外运行时依赖。

### 6. 升级识别
- INFO 的 `version` 末尾修订号（如 `-8`）递增，DSM 才会识别为「升级」而非重装。

### 7. 连接加速
- Token 模式没有本地 config.yml，加速参数必须作为 `cloudflared tunnel` **父命令**标志传入（`--protocol` / `--edge-ip-version` / `--ha-connections` / `--quic-disable-pmtu-discovery`），再跟 `run`。
- 诊断只看最近一次 `Initial protocol` 之后的日志，避免旧回退记录干扰当前提示。
- 设置落在 `var/accel.json`，升级套件会保留。

## 构建

```bash
# 默认 x86_64
./synology/build-spk.sh

# arm64 机型
GOARCH_TARGET=arm64 OUT_NAME=armv8 SPK_ARCH="rtd1296 armv8" ./synology/build-spk.sh
```

## 已知问题 / 风险

- 从 `-5` 升级到 `-6` 会保留旧版创建的管理登录文件（已不再使用），可直接忽略。
- 极少数 DSM 版本安装时不弹向导，此时需手动写入 Token 后重启套件。
