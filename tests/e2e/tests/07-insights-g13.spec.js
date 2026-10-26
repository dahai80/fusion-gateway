// G13/AC4/AC5/AC18 acceptance surfaces — capacity page, explorer prefill +
// replay/chunks, compare overlap + breaker trips, latency p99, persistence
// stream_chunks round-trip. Read-only except the persistence PUT, which
// writes the same values back (fixtures.js backs up / restores config.yaml).

const { test, expect, login, adminApi, openAsAdmin } = require('../fixtures');

const WINDOW = (hours) => {
    const now = new Date();
    return {
        from: new Date(now.getTime() - hours * 3600 * 1000).toISOString(),
        to: now.toISOString(),
    };
};

test.describe('g13: capacity page', () => {
    test('capacity page renders heatmap + HW chart cards', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/capacity');
        await expect(page.getByRole('heading', { name: 'Capacity' })).toBeVisible({ timeout: 15_000 });
        await expect(page.locator('.ant-card-head').filter({ hasText: 'Request Volume Heatmap' })).toBeVisible();
        await expect(page.locator('.ant-card-head').filter({ hasText: 'Hardware Capacity' })).toBeVisible();
        // 7 day rows × 24 hour cells
        await expect(page.locator('table tbody tr').first()).toBeVisible();
        expect(await page.locator('table tbody tr').count()).toBe(7);
    });
});

test.describe('ac4/ac5: compare extensions', () => {
    test('compare response carries breaker_trips array', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const w = WINDOW(24 * 7);
        const res = await api('GET', `/insights/compare?from=${w.from}&to=${w.to}`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(Array.isArray(body.breaker_trips)).toBe(true);
    });

    test('compare overlap + input bucket params accepted', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const w = WINDOW(24 * 7);
        const res = await api('GET',
            `/insights/compare?from=${w.from}&to=${w.to}&overlap=true&input_min=100&input_max=100000`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(Array.isArray(body.rows)).toBe(true);
        expect(body.overlap).toBe(true);
    });

    test('compare page shows Diff + Breaker Trips columns', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/compare');
        await expect(page.getByRole('heading', { name: 'Provider Comparison' })).toBeVisible({ timeout: 15_000 });
        await expect(page.locator('.ant-table-thead').getByText('Diff')).toBeVisible();
        await expect(page.locator('.ant-table-thead').getByText('Breaker Trips')).toBeVisible();
    });
});

test.describe('g8: latency p99', () => {
    test('latency buckets carry ttft_p99_ms + tpot_p99_ms', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const w = WINDOW(24);
        const res = await api('GET', `/insights/latency?from=${w.from}&to=${w.to}&granularity=hour`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        for (const b of body.series || []) {
            expect(typeof b.ttft_p99_ms).toBe('number');
            expect(typeof b.tpot_p99_ms).toBe('number');
        }
    });

    test('latency page shows p99 legend + anomaly toggle', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/latency');
        await expect(page.getByRole('heading', { name: 'Latency (TTFT / TPOT)' })).toBeVisible({ timeout: 15_000 });
        await expect(page.getByText('ms/token').first()).toBeVisible();
        await expect(page.getByText('anomalies', { exact: true }).first()).toBeVisible();
    });
});

test.describe('ac18/g3/g10: explorer', () => {
    test('explorer prefill via URL params runs a search', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/insights/explorer?period=24');
        await expect(page.getByRole('heading', { name: 'Log Explorer' })).toBeVisible({ timeout: 15_000 });
        // prefill fires runSearch immediately — the results table (or its
        // empty state) replaces the pre-search blank
        await expect(page.locator('.ant-table').first()).toBeVisible({ timeout: 20_000 });
        await expect(page.locator('.ant-spin-spinning').first()).toBeHidden({ timeout: 20_000 });
    });

    test('chunks endpoint answers 200 with array for a stored log', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const w = WINDOW(24);
        const search = await api('POST', '/insights/logs/search', { from: w.from, to: w.to, limit: 5, offset: 0 });
        const rows = (await search.json()).rows || [];
        test.skip(rows.length === 0, 'no stored logs in the last 24h');
        const res = await api('GET', `/insights/logs/${rows[0].id}/chunks`);
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(Array.isArray(body.chunks)).toBe(true);
    });

    test('replay endpoint on a stored log returns endpoint+status', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const w = WINDOW(24);
        const search = await api('POST', '/insights/logs/search', { from: w.from, to: w.to, limit: 5, offset: 0 });
        expect(search.status()).toBe(200);
        const rows = (await search.json()).rows || [];
        test.skip(rows.length === 0, 'no stored logs in the last 24h to replay');
        const res = await api('POST', `/insights/logs/${rows[0].id}/replay`);
        // replay surfaces the upstream outcome, whatever it is — never a 5xx
        // gateway crash. A stored log without a captured body answers 409.
        expect([200, 409, 422].includes(res.status())).toBe(true);
        if (res.status() === 200) {
            const body = await res.json();
            expect(typeof body.endpoint).toBe('string');
            expect(typeof body.status).toBe('number');
        }
    });
});

test.describe('g13: persistence stream_chunks round-trip', () => {
    test('GET includes image_placeholder + stream_chunks + stream_chunks_days', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/config/persistence');
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(typeof body.capture.image_placeholder).toBe('boolean');
        expect(typeof body.capture.stream_chunks).toBe('boolean');
        expect(typeof body.retention.stream_chunks_days).toBe('number');
    });

    test('PUT round-trips the new capture/retention fields', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const get = await api('GET', '/config/persistence');
        const current = await get.json();
        const put = await api('PUT', '/config/persistence', current);
        expect(put.status()).toBe(200);
        const after = await put.json();
        expect(after.capture.image_placeholder).toBe(current.capture.image_placeholder);
        expect(after.capture.stream_chunks).toBe(current.capture.stream_chunks);
        expect(after.retention.stream_chunks_days).toBe(current.retention.stream_chunks_days);
    });

    test('persistence page shows the two capture toggles', async ({ page }) => {
        const { token } = await login(page);
        await openAsAdmin(page, token, '/admin/config/persistence');
        await expect(page.locator('.ant-card-head').filter({ hasText: 'Persistence Configuration' })).toBeVisible({ timeout: 15_000 });
        await expect(page.locator('.ant-form-item').filter({ hasText: 'Image Placeholder' })).toBeVisible();
        await expect(page.locator('.ant-form-item').filter({ hasText: 'Stream Chunk Timing Capture' })).toBeVisible();
        await expect(page.locator('.ant-form-item').filter({ hasText: 'Stream Chunks' }).first()).toBeVisible();
    });
});
