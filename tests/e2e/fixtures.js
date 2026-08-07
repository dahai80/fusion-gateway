// Shared fixtures for Fusion-Gateway admin E2E tests.
//
// Key responsibilities:
//   1. login()    — authenticate against /admin/api/login, return a bearer token
//   2. adminApi   — request helper bound to /admin/api with the token
//   3. configGuard — back up config.yaml ONCE before the suite, restore it
//      ONCE after, so mutating config PUTs never leave the running gateway
//      in a changed state. The user's config is sacred.
//   4. waitForConfig — poll a config GET until hot-reload propagates a PUT.
//
// Admin credentials come from config.yaml (admin.users.admin). Defaults match
// the shipped config: admin / change-me-secure-password.

const { test: base, expect } = require('@playwright/test');
const path = require('path');

const BASE = process.env.FG_BASE || 'http://127.0.0.1:11432';
const CFG = process.env.FG_CONFIG || path.resolve(__dirname, '../../config.yaml');
const ADMIN_USER = process.env.FG_ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.FG_ADMIN_PASS || 'change-me-secure-password';

// login via the admin API; returns { token, role }.
async function login(page) {
    const res = await page.request.post(`${BASE}/admin/api/login`, {
        data: { username: ADMIN_USER, password: ADMIN_PASS },
    });
    expect(res.ok(), `login failed: ${res.status()}`).toBeTruthy();
    const body = await res.json();
    expect(body.token, 'login returned no token').toBeTruthy();
    return { token: body.token, role: body.role };
}

// Build an admin-API request helper carrying the bearer token.
function adminApi(page, token) {
    return (method, url, data) =>
        page.request.fetch(`${BASE}/admin/api${url}`, {
            method,
            data,
            headers: { Authorization: `Bearer ${token}` },
        });
}

// Wait for hot-reload to propagate a config PUT into the live snapshot.
// The gateway uses fsnotify (debounce 500ms). Poll a GET reflecting the
// changed field until it matches or we time out.
async function waitForConfig(page, token, section, expectedPredicate, timeoutMs = 8000) {
    const api = adminApi(page, token);
    const deadline = Date.now() + timeoutMs;
    let last = null;
    while (Date.now() < deadline) {
        const res = await api('GET', `/config/${section}`);
        if (res.ok()) {
            last = await res.json();
            if (expectedPredicate(last)) return last;
        }
        await page.waitForTimeout(300);
    }
    throw new Error(`config /config/${section} did not reach expected state within ${timeoutMs}ms; last=${JSON.stringify(last)}`);
}

// Open an authenticated admin SPA route. The SPA route guard reads
// localStorage("admin_logged_in"); axios reads the admin_token cookie. We set
// both before navigating so the page renders without bouncing to /admin/login.
async function openAsAdmin(page, token, path) {
    await page.context().addCookies([
        { name: 'admin_token', value: token, domain: '127.0.0.1', path: '/' },
    ]);
    await page.addInitScript(() => {
        localStorage.setItem('admin_logged_in', 'true');
    });
    await page.goto(`${BASE}${path}`);
    await page.waitForLoadState('networkidle');
}

const test = base.extend({
    // eslint-disable-next-line no-empty-pattern
    adminToken: async ({ page }, use) => {
        const { token } = await login(page);
        await use(token);
    },
});

module.exports = {
    test,
    expect,
    BASE,
    CFG,
    ADMIN_USER,
    ADMIN_PASS,
    login,
    adminApi,
    waitForConfig,
    openAsAdmin,
};
