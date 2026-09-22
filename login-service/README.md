# ds2api-login-service

基于无头 Chromium 的 DeepSeek 登录服务。用于绕过 `POST /api/v0/users/login` 上的 AWS WAF JS challenge。

## 为什么需要它

DeepSeek 登录端点被 AWS WAF 保护。任何**代码发起的请求**（`fetch` / XHR / 纯 HTTP 客户端）——即使带着全套有效 cookie——都会收到 `x-amzn-waf-action: challenge`（HTTP 202 空响应）。只有真实浏览器里的**原生表单提交**能通过。

本服务用 Playwright 驱动无头 Chromium，像真人一样填写登录表单并点击提交，让浏览器自己处理 WAF challenge 和设备指纹，然后把会话 token 返回给调用方。

## 运行

### 本地开发（有显示器）

```bash
cd login-service
npm install
npx playwright install chromium
npm start                          # 默认有头模式，会打开浏览器窗口
```

如需 headless 模式（不推荐，会被 CloudFront WAF 拦截）：

```bash
LOGIN_HEADLESS=1 npm start
```

### Docker（无头服务器，通过 xvfb 虚拟显示）

```bash
docker build -t ds2api-login-service .
docker run -p 8787:8787 ds2api-login-service
```

`mcr.microsoft.com/playwright` 镜像自带 xvfb-run，CMD 会以 `xvfb-run -a node index.js` 启动，在虚拟显示上跑有头 Chromium。已确认能通过 CloudFront WAF + AWS WAF challenge。

> **为什么不能 headless？** CloudFront Bot Control 会检测无头 Chromium，直接返回 `ERROR: The request could not be satisfied`。有头模式（配合反检测参数 `--disable-blink-features=AutomationControlled` + 隐藏 `navigator.webdriver`）才能通过。

## 接口

### `GET /health`

```json
{"ok": true}
```

### `POST /login`

请求体：

```json
{"email": "user@example.com", "password": "xxx"}
```

或手机号：

```json
{"mobile": "13800138000", "password": "xxx"}
```

成功：

```json
{"success": true, "token": "GXv0yobXx..."}
```

失败：

```json
{"success": false, "error": "RISK_DEVICE_DETECTED"}
```

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `LOGIN_SERVICE_HOST` | `127.0.0.1` | 监听地址 |
| `LOGIN_SERVICE_PORT` | `8787` | 监听端口 |
| `LOGIN_HEADLESS` | 空（有头） | `1`/`true` 时用 headless 模式（会被 WAF 拦） |
| `LOGIN_SETTLE_MS` | `4000` | 页面加载后等待 WAF challenge 完成的毫秒数 |
| `LOGIN_TIMEOUT_MS` | `20000` | 点击提交后等待登录响应的毫秒数 |

## ds2api 对接

在 ds2api 的 `config.json` 里设置：

```json
{
  "login_service_url": "http://127.0.0.1:8787"
}
```

设置后，ds2api 的账号登录会改调本服务获取 token，不再直连 DeepSeek 登录接口。
