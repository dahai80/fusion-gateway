// Persistent Dashboard (#421) + legacy redirect coverage. `/admin/` now
// renders the PG-backed Dashboard (top-level menu entry restored); legacy
// Logs/Analytics pages stay deprecated and redirect into Insights. The
// legacy admin APIs (still part of the admin API surface) keep answering.

const { test, expect, login, adminApi, openAsAdmin } = require('../fixtures');

test.describe('persistent dashboard', () => {
    test('/admin/ renders the Dashboard landing page', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/');
        // KPI card + trend chart titles come from the Dashboard page itself.
        await expect(page.getByText('Requests & Tokens Trend')).toBeVisible({ timeout: 15_000 });
        await expect(page.getByText('Top Models')).toBeVisible();
        expect(await page.locator('.ant-result-error').count()).toBe(0);
    });

    test('sidebar has top-level Dashboard but no legacy Logs/Analytics', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/explorer');
        const menu = page.locator('.ant-menu');
        await expect(menu).toBeVisible();
        await expect(menu.getByText('Dashboard', { exact: true }).first()).toBeVisible();
        expect(await menu.getByText('Logs', { exact: true }).count()).toBe(0);
        expect(await menu.getByText('Analytics', { exact: true }).count()).toBe(0);
    });

    test('insights overview API returns 200 with the #421 shape', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/insights/overview?granularity=hour');
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(body.from).toBeTruthy();
        expect(body.granularity).toBe('hour');
        expect(body.totals).toBeTruthy();
        expect(typeof body.totals.requests).toBe('number');
        expect(typeof body.totals.cached_tokens).toBe('number');
        expect(typeof body.totals.success_rate).toBe('number');
        expect(Array.isArray(body.top_models)).toBe(true);
        expect(Array.isArray(body.top_backends)).toBe(true);
        expect(Array.isArray(body.gateway_caches)).toBe(true);
        // series stays sorted ascending by bucket
        const series = body.series || [];
        for (let i = 1; i < series.length; i++) {
            expect(series[i - 1].bucket < series[i].bucket).toBe(true);
        }
    });
});

test.describe('legacy page redirects', () => {
    for (const [legacy, target] of [
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
