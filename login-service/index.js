'use strict';

// ds2api-login-service
//
// A tiny HTTP service that performs the DeepSeek web login in a real
// headless browser. DeepSeek protects POST /api/v0/users/login with an AWS
// WAF JS challenge: any programmatic request (fetch / XHR / plain HTTP
// client) is answered with `x-amzn-waf-action: challenge`, even when it
// carries a full set of cookies. Only a native form submission in a real
// browser passes. This service therefore drives the sign-in form the way a
// human would, lets the browser handle the WAF challenge + device
// fingerprinting on its own, and returns the session token.
//
// Endpoints:
//   GET  /health              -> {"ok":true}
//   POST /login               -> {"success":true,"token":"..."} or
//                                {"success":false,"error":"..."}
//
// Request body for /login (JSON):
//   { "email": "...", "password": "..." }
//   or { "mobile": "...", "password": "..." }

const http = require('http');
const { chromium } = require('playwright');

const PORT = process.env.LOGIN_SERVICE_PORT ? Number(process.env.LOGIN_SERVICE_PORT) : 8787;
const HOST = process.env.LOGIN_SERVICE_HOST || '127.0.0.1';

// How long (ms) to wait for the WAF challenge / first paint to settle after
// navigation before we start filling the form.
const SETTLE_MS = Number(process.env.LOGIN_SETTLE_MS || 4000);
// How long (ms) to wait after clicking submit for the login API response.
const LOGIN_TIMEOUT_MS = Number(process.env.LOGIN_TIMEOUT_MS || 20000);

const SIGN_IN_URL = 'https://chat.deepseek.com/sign_in';

function readJSON(req) {
  return new Promise((resolve, reject) => {
    let data = '';
    req.on('data', (chunk) => {
      data += chunk;
      if (data.length > 1e6) {
        reject(new Error('body too large'));
        req.destroy();
      }
    });
    req.on('end', () => {
      if (!data) return resolve({});
      try {
        resolve(JSON.parse(data));
      } catch (e) {
        reject(new Error('invalid JSON body'));
      }
    });
    req.on('error', reject);
  });
}

function sendJSON(res, status, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  });
  res.end(body);
}

// DeepSeek sign-in form (React SPA, no native <form>/<button>):
//   - credential input:  <input type="text"  placeholder="请输入手机号/邮箱地址">
//   - password input:    <input type="password" placeholder="请输入密码">
//   - submit "button":   <DIV role="button" class="ds-button--primary">登录</DIV>

async function fillCredentialField(page, email, mobile) {
  // DeepSeek uses a single unified input for both email and phone. Wait for
  // it to appear (WAF challenge + SPA render can take several seconds).
  const placeholders = [/手机号|邮箱|email|phone/i];
  for (const re of placeholders) {
    const loc = page.getByPlaceholder(re).first();
    try {
      await loc.waitFor({ state: 'visible', timeout: 15000 });
      const value = email || mobile;
      await loc.fill(value);
      return true;
    } catch (_) { /* try next */ }
  }
  return false;
}

async function fillPasswordField(page, password) {
  const loc = page.locator('input[type="password"]').first();
  if ((await loc.count()) > 0 && (await loc.isVisible())) {
    await loc.fill(password);
    return true;
  }
  return false;
}

async function clickSubmitButton(page) {
  const loc = page.getByRole('button', { name: /登录|Log in/i }).first();
  if ((await loc.count()) > 0 && (await loc.isVisible())) {
    await loc.click();
    return true;
  }
  return false;
}

async function performLogin(email, mobile, password) {
  // Headless Chromium is detected and blocked by CloudFront/WAF ("The
  // request could not be satisfied"), so default to headed mode with
  // anti-detection flags. On a headless server run under xvfb-run.
  const headless = process.env.LOGIN_HEADLESS === '1' || process.env.LOGIN_HEADLESS === 'true';
  const browser = await chromium.launch({
    headless,
    args: [
      '--disable-blink-features=AutomationControlled',
      '--no-sandbox',
      '--disable-dev-shm-usage',
    ],
  });
  try {
    const context = await browser.newContext({
      userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36',
      viewport: { width: 1366, height: 768 },
      locale: 'zh-CN',
      timezoneId: 'Asia/Shanghai',
    });
    const page = await context.newPage();

    // Hide automation signals.
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
    });

    let token = null;
    let loginError = null;

    page.on('response', async (resp) => {
      if (!resp.url().includes('/api/v0/users/login')) return;
      try {
        const body = await resp.json();
        const code = body && body.code;
        const bizData = body && body.data && body.data.biz_data;
        const user = bizData && bizData.user;
        if (resp.status() === 200 && code === 0 && user && user.token) {
          token = user.token;
        } else if (resp.status() === 200 && code !== 0) {
          loginError = (body && body.msg) || 'login rejected';
        } else if (bizData && bizData.biz_code !== 0) {
          loginError = bizData.biz_msg || body.data.biz_msg || 'login rejected';
        }
      } catch (_) {
        // non-JSON or aborted response; ignore
      }
    });

    await page.goto(SIGN_IN_URL, { waitUntil: 'domcontentloaded', timeout: 60000 });
    // Give the AWS WAF challenge and the Shumei fingerprint JS time to run.
    await page.waitForTimeout(SETTLE_MS);

    // Fill the credential field.
    const filled = await fillCredentialField(page, email, mobile);
    if (!filled) {
      await page.screenshot({ path: '/tmp/login-debug.png', fullPage: true }).catch(() => {});
      const title = await page.title().catch(() => '?');
      const url = page.url();
      throw new Error(`login form not found (url=${url}, title=${title})`);
    }

    if (!(await fillPasswordField(page, password))) {
      throw new Error('login form not found (password input missing)');
    }

    if (!(await clickSubmitButton(page))) {
      throw new Error('login form not found (submit button missing)');
    }

    // Wait for the login API response (or timeout).
    const deadline = Date.now() + LOGIN_TIMEOUT_MS;
    while (Date.now() < deadline) {
      if (token) return token;
      if (loginError) throw new Error(loginError);
      await page.waitForTimeout(250);
    }
    if (token) return token;
    throw new Error(loginError || 'login timed out');
  } finally {
    await browser.close();
  }
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host || 'localhost'}`);

  if (req.method === 'GET' && url.pathname === '/health') {
    return sendJSON(res, 200, { ok: true });
  }

  if (req.method === 'POST' && url.pathname === '/login') {
    let body;
    try {
      body = await readJSON(req);
    } catch (e) {
      return sendJSON(res, 400, { success: false, error: e.message });
    }
    const email = String(body.email || '').trim();
    const mobile = String(body.mobile || '').trim();
    const password = String(body.password || '');
    if (!email && !mobile) {
      return sendJSON(res, 400, { success: false, error: 'missing email/mobile' });
    }
    if (!password) {
      return sendJSON(res, 400, { success: false, error: 'missing password' });
    }
    try {
      const token = await performLogin(email, mobile, password);
      return sendJSON(res, 200, { success: true, token });
    } catch (e) {
      // eslint-disable-next-line no-console
      console.error('login error:', e && e.message ? e.message : e);
      return sendJSON(res, 401, { success: false, error: String(e && e.message ? e.message : e) });
    }
  }

  return sendJSON(res, 404, { success: false, error: 'not found' });
});

server.listen(PORT, HOST, () => {
  // eslint-disable-next-line no-console
  console.log(`ds2api-login-service listening on http://${HOST}:${PORT}`);
});
