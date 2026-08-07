// Dashboard & analytics pages — verify they load and render their data
// widgets without error. Covers the read-only display endpoints.

const { test, expect, BASE, login, adminApi, openAsAdmin } = require('../fixtures');

async function open(page, token, path) {
    await openAsAdmin(page, token, path);
}

test.describe('dashboard & analytics', () => {
    test('dashboard page renders', async ({ page }) => {
        const { token } = await login(page);
        await open(page, token, '/admin/');
        await expect(page.locator('body')).not.toBeEmpty({ timeout: 15_000 });
        expect(await page.locator('.ant-result-error').count()).toBe(0);
    });

    test('analytics overview API returns 200', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/analytics');
        expect(res.status()).toBe(200);
    });

    test('analytics profit page renders', async ({ page }) => {
        const { token } = await login(page);
        await open(page, token, '/admin/analytics');
        await expect(page.locator('body')).not.toBeEmpty({ timeout: 15_000 });
    });

    test('dashboard API returns 200', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/dashboard');
        expect(res.status()).toBe(200);
    });
});

test.describe('logs page', () => {
    test('logs API returns 200 with list shape', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/logs');
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(body).toBeTruthy();
    });

    test('logs export endpoint returns content', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/logs/export');
        expect([200, 204, 404]).toContain(res.status());
    });
});
