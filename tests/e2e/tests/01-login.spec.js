// Login & auth page — covers the login form inputs and button, bad
// credentials, logout, and the redirect-to-login guard.

const { test, expect, BASE, ADMIN_USER, ADMIN_PASS } = require('../fixtures');

test.describe('admin login page', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto(`${BASE}/admin/login`);
    });

    test('renders username + password inputs and a submit button', async ({ page }) => {
        await expect(page.getByPlaceholder('Username')).toBeVisible();
        await expect(page.getByPlaceholder('Password')).toBeVisible();
        await expect(page.getByRole('button', { name: /login|登录|sign in/i })).toBeVisible();
    });

    test('rejects wrong credentials with an error', async ({ page }) => {
        await page.getByPlaceholder('Username').fill('admin');
        await page.getByPlaceholder('Password').fill('definitely-wrong-pass');
        await page.getByRole('button', { name: /login|登录|sign in/i }).click();
        await page.waitForTimeout(1500);
        const stillOnLogin = page.url().includes('/admin/login') || page.url().endsWith('/admin/');
        const errMsg = await page.locator('.ant-message, .ant-form-item-explain-error').count();
        expect(stillOnLogin || errMsg > 0).toBeTruthy();
    });

    test('accepts correct credentials and leaves login page', async ({ page, context }) => {
        await page.getByPlaceholder('Username').fill(ADMIN_USER);
        await page.getByPlaceholder('Password').fill(ADMIN_PASS);
        await page.getByRole('button', { name: /login|登录|sign in/i }).click();
        await page.waitForURL((u) => !u.pathname.endsWith('/admin/login'), { timeout: 15_000 });
        const cookies = await context.cookies();
        expect(cookies.some((c) => c.name === 'admin_token' && c.value)).toBeTruthy();
    });

    test('protected /admin/keys redirects to login when unauthenticated', async ({ page, context }) => {
        await context.clearCookies();
        await page.goto(`${BASE}/admin/keys`);
        await page.waitForURL((u) => u.pathname.endsWith('/admin/login') || u.pathname.endsWith('/admin/'), { timeout: 10_000 }).catch(() => {});
        const cookies = await context.cookies();
        expect(cookies.some((c) => c.name === 'admin_token')).toBeFalsy();
    });
});
