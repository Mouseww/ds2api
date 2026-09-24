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
const { spawn } = require('child_process');
const crypto = require('crypto');
const { chromium } = require('playwright');

const PORT = process.env.LOGIN_SERVICE_PORT ? Number(process.env.LOGIN_SERVICE_PORT) : 8787;
const HOST = process.env.LOGIN_SERVICE_HOST || '127.0.0.1';

// Path to Chromium binary (bundled with Playwright Docker image).
const CHROME_PATH = process.env.CHROME_PATH || '/ms-playwright/chromium-1200/chrome-linux64/chrome';

// Optional egress proxy for the browser (e.g. "http://host:port" or
// "socks5://host:port"). DeepSeek risk control flags by IP, so routing the
// login through a clean residential/datacenter IP is the real fix for
// RISK_DEVICE_DETECTED.
const LOGIN_PROXY = process.env.LOGIN_PROXY || '';

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

// Human-like interaction helpers. The Shumei fingerprint SDK (fp.min.js)
// collects behavioral biometrics — keystroke dynamics, mouse trajectories,
// focus events. page.fill() sets values with zero keyboard events, which the
// SDK flags as automation, poisoning the smidV2 and causing
// RISK_DEVICE_DETECTED. These helpers reproduce real user behavior instead.

async function humanClick(page, loc) {
  try {
    const box = await loc.boundingBox();
    if (box) {
      const tx = box.x + box.width / 2;
      const ty = box.y + box.height / 2;
      // Move the mouse toward the target in a few small steps.
      const steps = 3 + Math.floor(Math.random() * 3);
      for (let i = 1; i <= steps; i++) {
        await page.mouse.move(
          tx * i / steps + (Math.random() - 0.5) * 6,
          ty * i / steps + (Math.random() - 0.5) * 6,
        );
        await page.waitForTimeout(30 + Math.random() * 60);
      }
      await page.mouse.move(tx, ty);
      await page.waitForTimeout(40 + Math.random() * 80);
    }
  } catch (_) { /* boundingBox can fail on detached elements */ }
  await loc.click();
  await page.waitForTimeout(150 + Math.random() * 350);
}

async function humanType(page, loc, text) {
  await humanClick(page, loc); // focus the input with a real click first
  for (const ch of text) {
    await page.keyboard.type(ch);
    // Natural typing rhythm: 40-170ms between keystrokes.
    await page.waitForTimeout(40 + Math.random() * 130);
  }
  await page.waitForTimeout(200 + Math.random() * 400);
}

async function clickPasswordLoginToggle(page) {
  // The sign-in page defaults to phone + verification-code login. Switch to
  // the email/password form by clicking the "密码登录" toggle link.
  const selectors = [
    'div.ds-sign-in-form__social-link:has-text("密码登录")',
    'div.ds-sign-in-form__social-link:has-text("password")',
    '[role="button"]:has-text("密码登录")',
    '[role="button"]:has-text("password")',
    'text="密码登录"',
    'text="password"',
  ];
  for (const sel of selectors) {
    try {
      const loc = page.locator(sel).first();
      await loc.waitFor({ state: 'visible', timeout: 8000 });
      await humanClick(page, loc);
      // Confirm the password form replaced the verification-code form.
      const pw = page.locator('input[type="password"]').first();
      await pw.waitFor({ state: 'visible', timeout: 8000 });
      return true;
    } catch (_) { /* try next selector */ }
  }
  return false;
}

async function fillCredentialField(page, email, mobile) {
  // DeepSeek uses a single unified input for both email and phone. Wait for
  // it to appear (WAF challenge + SPA render can take several seconds).
  const placeholders = [/手机号|邮箱|email|phone/i];
  for (const re of placeholders) {
    const loc = page.getByPlaceholder(re).first();
    try {
      await loc.waitFor({ state: 'visible', timeout: 15000 });
      const value = email || mobile;
      await humanType(page, loc, value);
      return true;
    } catch (_) { /* try next */ }
  }
  return false;
}

async function fillPasswordField(page, password) {
  const loc = page.locator('input[type="password"]').first();
  try {
    await loc.waitFor({ state: 'visible', timeout: 15000 });
    await humanType(page, loc, password);
    return true;
  } catch (_) { return false; }
}

async function clickSubmitButton(page) {
  const loc = page.getByRole('button', { name: /登录|Log in/i }).first();
  try {
    await loc.waitFor({ state: 'visible', timeout: 15000 });
    await humanClick(page, loc);
    return true;
  } catch (_) { return false; }
}

// launchCleanBrowser starts Chromium manually (not through Playwright's launch)
// so it does NOT add --enable-automation or --remote-debugging-pipe, both of
// which are red flags for risk-control / device-fingerprint detection.
async function launchCleanBrowser(userDataDir) {
  const port = 9000 + Math.floor(Math.random() * 2000);
  const args = [
    `--user-data-dir=${userDataDir}`,
    `--remote-debugging-port=${port}`,
    `--window-size=1366,768`,
    `--window-position=0,0`,
    '--no-sandbox',
    '--disable-dev-shm-usage',
    '--disable-blink-features=AutomationControlled',
    '--disable-features=UserAgentClientHint',
    '--disable-infobars',
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-sync',
    '--disable-background-networking',
    '--disable-default-apps',
    '--disable-extensions',
    '--disable-component-update',
    '--disable-gpu',
    '--disable-search-engine-choice-screen',
    '--lang=zh-CN',
    '--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36',
  ];
  if (LOGIN_PROXY) {
    args.push(`--proxy-server=${LOGIN_PROXY}`);
  }
  args.push('about:blank');
  const chrome = spawn(CHROME_PATH, args, { stdio: 'ignore', detached: true });
  chrome.unref();

  // Wait for the CDP endpoint to become available.
  const cdp = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(`${cdp}/json/version`);
      if (r.ok) break;
    } catch (_) { /* not ready yet */ }
    await new Promise(r => setTimeout(r, 250));
  }

  const browser = await chromium.connectOverCDP(cdp);
  const context = browser.contexts()[0];

  // Anti-detection JS injections.
  await context.addInitScript(`
    // Remove webdriver flag
    Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
    // Canvas noise: flip lowest alpha bit for large reads
    const origGetImageData = CanvasRenderingContext2D.prototype.getImageData;
    CanvasRenderingContext2D.prototype.getImageData = function(x, y, w, h) {
      const d = origGetImageData.call(this, x, y, w, h);
      if (w * h > 50) {
        for (let i = 3; i < d.data.length; i += Math.max(1, Math.floor(w * h / 200))) d.data[i] ^= 1;
      }
      return d;
    };
    // WebGL noise: append zero-width char to renderer string
    try {
      const origGP = WebGLRenderingContext.prototype.getParameter;
      WebGLRenderingContext.prototype.getParameter = function(p) {
        const r = origGP.call(this, p);
        if (p === 37446 && typeof r === 'string') return r + '\\u200b';
        return r;
      };
    } catch (_) {}
    // Audio noise: sparse perturbation
    try {
      const origGCD = AudioBuffer.prototype.getChannelData;
      AudioBuffer.prototype.getChannelData = function(c) {
        const d = origGCD.call(this, c);
        for (let i = 0; i < d.length; i += 997) d[i] += (Math.random() - 0.5) * 1e-12;
        return d;
      };
    } catch (_) {}
  `);

  return { browser, context, chrome, port };
}

async function performLogin(email, mobile, password) {
  const id = email || mobile;
  const profileHash = crypto.createHash('md5').update(id).digest('hex').slice(0, 12);
  // Persistent profile directory (mounted as a volume so the device
  // fingerprint survives container restarts).
  const profileRoot = process.env.LOGIN_PROFILE_DIR || '/data/profiles';
  const userDataDir = `${profileRoot}/ds-login-profile-${profileHash}`;

  // Stable per-profile x-device-id (UUID v4), persisted so the same account
  // always reuses the same header device id.
  const xDeviceIdFile = `${userDataDir}/x-device-id.txt`;
  let xDeviceId = null;
  try {
    const fs = require('fs');
    if (fs.existsSync(xDeviceIdFile)) {
      xDeviceId = fs.readFileSync(xDeviceIdFile, 'utf8').trim();
    }
  } catch (_) {}
  if (!xDeviceId || !/^[0-9a-f]{8}-[0-9a-f-]{27,}$/i.test(xDeviceId)) {
    xDeviceId = crypto.randomUUID();
    try {
      require('fs').mkdirSync(userDataDir, { recursive: true });
      require('fs').writeFileSync(xDeviceIdFile, xDeviceId);
    } catch (_) {}
  }

  const { browser, context, chrome } = await launchCleanBrowser(userDataDir);
  try {
    const page = await context.newPage();

    // Hide automation signals.
    let token = null;
    let loginError = null;
    let capturedDeviceId = null;

    // Capture the device_id the browser sends in the login request body.
    page.on('request', (req) => {
      if (req.method() === 'POST' && req.url().includes('/api/v0/users/login')) {
        try {
          const body = JSON.parse(req.postData() || '{}');
          if (body.device_id) capturedDeviceId = body.device_id;
        } catch (_) {}
      }
    });

    page.on('response', async (resp) => {
      if (!resp.url().includes('/api/v0/users/login')) return;
      try {
        const body = await resp.json();
        const code = body && body.code;
        const data = body && body.data;
        const bizCode = data && data.biz_code;
        const bizMsg = data && data.biz_msg;
        const bizData = data && data.biz_data;
        const user = bizData && bizData.user;
        // eslint-disable-next-line no-console
        console.log(`[login] resp: status=${resp.status()} code=${code} biz_code=${bizCode} biz_msg=${bizMsg || ''}`);
        if (resp.status() === 200 && code === 0 && user && user.token) {
          token = user.token;
        } else if (bizCode !== 0 && bizCode !== undefined) {
          loginError = bizMsg || ('biz_code=' + bizCode);
        } else if (resp.status() === 200 && code !== 0) {
          loginError = (body && body.msg) || 'login rejected';
        }
      } catch (_) {
        // eslint-disable-next-line no-console
        console.log('[login] non-JSON response url=' + resp.url());
      }
    });

    // eslint-disable-next-line no-console
    console.log('[login] goto sign_in');
    await page.goto(SIGN_IN_URL, { waitUntil: 'domcontentloaded', timeout: 60000 });
    await page.waitForTimeout(SETTLE_MS);
    // eslint-disable-next-line no-console
    console.log(`[login] loaded url=${page.url().slice(0, 60)} title="${await page.title().catch('?')}"`);

    // If the profile still has an active session, sign_in redirects to the
    // homepage. The stored token is valid — return it directly instead of
    // re-logging in (fastest path, no form interaction needed).
    if (!page.url().includes('sign_in')) {
      const stored = await page.evaluate(() => {
        try {
          const raw = localStorage.getItem('userToken');
          if (!raw) return null;
          const parsed = JSON.parse(raw);
          return parsed && parsed.value ? parsed.value : null;
        } catch (_) { return null; }
      }).catch(() => null);
      if (stored) {
        // Also grab the thumbcache cookie so ds2api can persist the device_id.
        let devId = '';
        try {
          const cookies = await context.cookies(['https://chat.deepseek.com']);
          const tc = cookies.find(c => c.name.startsWith('.thumbcache'));
          if (tc) devId = 'B' + decodeURIComponent(tc.value);
        } catch (_) {}
        // eslint-disable-next-line no-console
        console.log('[login] active session, returning stored token');
        return { token: stored, device_id: devId, x_device_id: xDeviceId };
      }
      // No stored token (logged out) — go back to the sign-in form.
      await page.goto(SIGN_IN_URL, { waitUntil: 'domcontentloaded', timeout: 60000 });
      await page.waitForTimeout(SETTLE_MS);
    }

    // Switch to password login if the default is phone + verification code.
    const toggled = await clickPasswordLoginToggle(page);
    // eslint-disable-next-line no-console
    console.log(`[login] toggle=${toggled}`);

    const filled = await fillCredentialField(page, email, mobile);
    // eslint-disable-next-line no-console
    console.log(`[login] credential filled=${filled}`);
    if (!filled) {
      await page.screenshot({ path: '/tmp/login-debug.png', fullPage: true }).catch(() => {});
      const title = await page.title().catch(() => '?');
      const url = page.url();
      throw new Error(`login form not found (url=${url}, title=${title})`);
    }

    const pwFilled = await fillPasswordField(page, password);
    // eslint-disable-next-line no-console
    console.log(`[login] pw filled=${pwFilled}`);
    if (!pwFilled) {
      await page.screenshot({ path: '/tmp/login-debug-pw.png', fullPage: true }).catch(() => {});
      const inputs = await page.evaluate(() => Array.from(document.querySelectorAll('input')).map(i => ({ type: i.type, placeholder: i.placeholder, visible: i.offsetParent !== null }))).catch(() => []);
      throw new Error('password input missing; inputs=' + JSON.stringify(inputs));
    }

    const submitted = await clickSubmitButton(page);
    // eslint-disable-next-line no-console
    console.log(`[login] submit clicked=${submitted}`);
    if (!submitted) {
      throw new Error('login form not found (submit button missing)');
    }

    // Wait for the login API response (or timeout).
    const deadline = Date.now() + LOGIN_TIMEOUT_MS;
    while (Date.now() < deadline) {
      if (token) break;
      if (loginError) throw new Error(loginError);
      await page.waitForTimeout(250);
    }
    if (token) {
      return { token, device_id: capturedDeviceId || '', x_device_id: xDeviceId };
    }
    throw new Error(loginError || 'login timed out');
  } finally {
    try { await browser.close(); } catch (_) {}
    // Kill the whole Chrome process GROUP. With detached:true Chrome becomes
    // a process-group leader, so -pid signals every child (renderers, GPU,
    // crashpad). chrome.kill() only signals the main process and leaks the
    // children as zombies under the container's PID 1.
    try { process.kill(-chrome.pid, 'SIGKILL'); } catch (_) {}
    try { process.kill(chrome.pid, 'SIGKILL'); } catch (_) {}
  }
}

// Concurrency limiter: each browser login spawns a full Chrome (~30 procs,
// ~500MB). Unbounded concurrency exhausts the host and degrades the Shumei
// behavioral biometrics (slow renders distort typing timing).
const MAX_CONCURRENT_LOGINS = Number(process.env.LOGIN_MAX_CONCURRENT || 2);
let activeLogins = 0;
const loginQueue = [];

function acquireLoginSlot() {
  return new Promise((resolve) => {
    if (activeLogins < MAX_CONCURRENT_LOGINS) {
      activeLogins++;
      resolve();
    } else {
      loginQueue.push(resolve);
    }
  });
}

function releaseLoginSlot() {
  activeLogins--;
  const next = loginQueue.shift();
  if (next) {
    activeLogins++;
    next();
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
      await acquireLoginSlot();
      try {
        const result = await performLogin(email, mobile, password);
        return sendJSON(res, 200, {
          success: true,
          token: result.token,
          device_id: result.device_id || '',
          x_device_id: result.x_device_id || '',
        });
      } finally {
        releaseLoginSlot();
      }
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
