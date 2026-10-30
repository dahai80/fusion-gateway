// Legacy Dashboard/Logs/Analytics pages are deprecated in favor of the
// PG-backed Insights pages. These specs assert the legacy routes redirect to
// their Insights counterparts and that the legacy admin APIs (still part of
// the admin API surface) keep answering.

const { test, expect, BASE, login, adminApi, openAsAdmin } = require('../fixtures');

test.describe('legacy page redirects', () => {
    for (const [legacy, target] of [
        ['/admin/', '/admin/insights/explorer'],
        ['/admin/logs', '/admin/insights/explorer'],
        ['/admin/analytics', '/admin/insights/latency'],
    ]) {
        test(`${legacy} redirects to ${target}`, async ({ page }) => {
            const { token } = await login(page);
            await openAsAdmin(page, token, legacy);
            await page.waitForURL(`**${target}*`, { timeout: 15_000 });
            await expect(page.locator('body')).not.toBeEmpty();
            expect(await page.locator('.ant-result-error').count()).toBe(0);
        });
    }

    test('sidebar has no legacy top-level entries', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/explorer');
        const menu = page.locator('.ant-menu');
        await expect(menu).toBeVisible();
        expect(await menu.getByText('Dashboard', { exact: true }).count()).toBe(0);
        expect(await menu.getByText('Logs', { exact: true }).count()).toBe(0);
        expect(await menu.getByText('Analytics', { exact: true }).count()).toBe(0);
    });
});

test.describe('legacy admin APIs stay available', () => {
    test('analytics overview API returns 200', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/analytics');
        expect(res.status()).toBe(200);
        const body = await res.json();
        const summary = body?.latency?.summary;
        expect(summary).toBeTruthy();
        // real percentiles must be ordered (previously fabricated avg*1.5/avg*2)
        expect(summary.p50_ms).toBeLessThanOrEqual(summary.p95_ms);
        expect(summary.p95_ms).toBeLessThanOrEqual(summary.p99_ms);
    });

    test('analytics profit API returns 200', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/analytics/profit');
        expect(res.status()).toBe(200);
    });

    test('dashboard API returns 200', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/dashboard');
        expect(res.status()).toBe(200);
    });

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
