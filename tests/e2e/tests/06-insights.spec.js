// Insights dashboards (P4) — the six persistence-backed admin pages plus the
// persistence config endpoint. Persistence is ON in the running gateway, so
// every insights endpoint must answer 200 (not the 503 "disabled" alert).
// No config mutation here beyond the persistence section round-trip, which
// writes the same values back (fixtures.js backs up / restores config.yaml).

const { test, expect, login, adminApi, openAsAdmin } = require('../fixtures');

test.describe('insights: API surface', () => {
    test('latency timeseries returns 200 with series', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const now = new Date();
        const from = new Date(now.getTime() - 24 * 3600 * 1000).toISOString();
        const res = await api('GET', `/insights/latency?from=${from}&to=${now.toISOString()}&granularity=hour`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(Array.isArray(body.series)).toBe(true);
        expect(body.granularity).toBe('hour');
    });

    test('compare returns 200 with rows array', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const now = new Date();
        const from = new Date(now.getTime() - 7 * 24 * 3600 * 1000).toISOString();
        const res = await api('GET', `/insights/compare?from=${from}&to=${now.toISOString()}`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(Array.isArray(body.rows)).toBe(true);
    });

    test('log search returns 200 with paged rows', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const now = new Date();
        const from = new Date(now.getTime() - 24 * 3600 * 1000).toISOString();
        const res = await api('POST', '/insights/logs/search', {
            from, to: now.toISOString(), limit: 5, offset: 0,
        });
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(typeof body.total).toBe('number');
        expect(Array.isArray(body.rows)).toBe(true);
        expect(body.rows.length).toBeLessThanOrEqual(5);
    });

    test('timeline returns 200 with four-track series (§15.2)', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const now = new Date();
        const from = new Date(now.getTime() - 24 * 3600 * 1000).toISOString();
        const res = await api('GET', `/insights/timeline?from=${from}&to=${now.toISOString()}&granularity=hour`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(Array.isArray(body.series)).toBe(true);
        expect(body.granularity).toBe('hour');
        // buckets are sorted ascending and carry all four tracks
        for (let i = 1; i < body.series.length; i++) {
            expect(body.series[i].bucket >= body.series[i - 1].bucket).toBe(true);
        }
        for (const b of body.series) {
            expect(typeof b.requests).toBe('number');
            expect(typeof b.errors).toBe('number');
            expect(typeof b.breaker_events).toBe('number');
            expect(typeof b.sys_mem_ratio).toBe('number');
            expect(typeof b.mlx_mem_ratio).toBe('number');
        }
    });

    test('anomalies + findings + reports list return 200', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        for (const ep of ['/anomalies?limit=5', '/optimization/findings?limit=5', '/reports?limit=5', '/insights/hw-samples?limit=2', '/events?limit=2']) {
            const res = await api('GET', ep);
            expect(res.status(), ep).toBe(200);
        }
    });
});

test.describe('insights: pages render', () => {
    const PAGES = [
        ['/admin/insights/latency', 'Latency (TTFT / TPOT)'],
        ['/admin/insights/compare', 'Provider Comparison'],
        ['/admin/insights/explorer', 'Log Explorer'],
        ['/admin/insights/optimize', 'Optimization Findings'],
        ['/admin/insights/anomalies', 'Log Anomalies'],
        ['/admin/insights/reports', 'Periodic Reports'],
    ];

    for (const [path, heading] of PAGES) {
        test(`${path} renders without the disabled alert`, async ({ page }) => {
            const { token } = await login(page);
            await openAsAdmin(page, token, path);
            await expect(page.getByRole('heading', { name: heading })).toBeVisible({ timeout: 15_000 });
            // persistence is enabled in the live gateway — the 503 alert must NOT show
            expect(await page.getByText('persistence is disabled', { exact: false }).count()).toBe(0);
            expect(await page.locator('.ant-result-error').count()).toBe(0);
        });
    }

    test('anomalies page renders the §15.2 timeline chart', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/anomalies');
        await expect(
            page.locator('.ant-card-head').filter({ hasText: 'Timeline — requests / errors / breaker events / memory' })
        ).toBeVisible({ timeout: 15_000 });
        // chart body: either the recharts surface or the empty-window alert
        const hasChart = await page.locator('.recharts-surface').count();
        const hasEmpty = await page.getByText('No traffic, events, or hardware samples', { exact: false }).count();
        expect(hasChart + hasEmpty).toBeGreaterThan(0);
    });
});

test.describe('persistence config', () => {
    test('GET /config/persistence returns full namespace', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/config/persistence');
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(typeof body.enabled).toBe('boolean');
        expect(body.postgres.dsn_env).toBeTruthy();
        expect(body.queue.capacity).toBeGreaterThan(0);
        expect(body.retention.request_logs_days).toBeGreaterThan(0);
    });

    test('PUT writes the same values back (idempotent round-trip)', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const get = await api('GET', '/config/persistence');
        expect(get.status()).toBe(200);
        const current = await get.json();
        const put = await api('PUT', '/config/persistence', current);
        expect(put.status()).toBe(200);
        const after = await put.json();
        expect(after.enabled).toBe(current.enabled);
        expect(after.postgres.dsn_env).toBe(current.postgres.dsn_env);
        expect(after.queue.capacity).toBe(current.queue.capacity);
        expect(after.retention.request_logs_days).toBe(current.retention.request_logs_days);
    });

    test('persistence config page renders', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/config/persistence');
        // ConfigPageLayout renders the title as an antd Card title, not a heading role
        await expect(page.locator('.ant-card-head').filter({ hasText: 'Persistence Configuration' })).toBeVisible({ timeout: 15_000 });
        await expect(page.locator('form')).toBeVisible();
    });
});
