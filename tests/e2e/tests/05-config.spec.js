// Configuration pages — the heart of "管理端配置后确保运行态满足管理态
// 的配置目标".
//
// Two layers:
//   A) For every config section, GET renders, PUT persists, and a follow-up
//      GET reflects the written value. Proves the admin page can read AND
//      write every configuration knob.
//   B) Runtime coupling: for config the live gateway enforces, verify the
//      gateway's behavior changes after the PUT:
//        - CORS:      set allowed_origins -> OPTIONS preflight echoes it
//        - Rate-limit: create a key with rpm:1 -> 2nd request gets 429
//
// config.yaml is backed up by fixtures.js and restored after the suite.

const { test, expect, BASE, login, adminApi, waitForConfig } = require('../fixtures');

const SECTIONS = [
    ['server', { log_level: 'debug' }],
    ['auth', { enabled: true }],
    ['rate-limit', { global_rpm: 1234 }],
    ['retry', { max_retries: 7, retryable_status_codes: [429, 500, 502, 503] }],
    ['negotiation', { disable_fusion_mlx_routing: true, route_header: 'X-Fusion-Route' }],
    ['cache', { enabled: false, max_entries: 50 }],
    ['cost', { enabled: false }],
    ['cost-markup', { enabled: false, global_markup: 0.1 }],
    ['pii', { enabled: false, action: 'block' }],
    ['cloud-routing', { strategy: 'weighted' }],
    ['hardware', { enabled: true, collect_interval: '5s' }],
    ['tokenizer', { provider: 'local', context_window_ratio: 0.9 }],
    ['observability', { metrics_enabled: true, log_format: 'json' }],
    ['cors', { allowed_origins: ['https://e2e-test.example.com'] }],
    ['hot-reload', { enabled: true, debounce: '500ms' }],
    ['cluster', { enabled: false, mode: 'standalone' }],
    ['realtime', { enabled: false, backend_url: 'http://localhost:8080' }],
    ['admin', { enabled: true, log_max_len: 10000 }],
    ['oidc', { enabled: false, issuer: 'https://example.com' }],
    ['rbac', { enabled: false, default_role: 'viewer' }],
    ['semantic-cache', { enabled: false, similarity_threshold: 0.95 }],
    ['prompt-injection', { enabled: false, action: 'block', threshold: 0.8 }],
    ['batch', { enabled: false, max_batch_size: 10 }],
    ['store', { backend: 'memory' }],
    ['validation', { base_url_conflict_check: true }],
];

test.describe('config: every section GET + PUT round-trip', () => {
    for (const [section, sample] of SECTIONS) {
        test(`/${section} GET renders and PUT persists`, async ({ page }) => {
            const { token } = await login(page);
            const api = adminApi(page, token);

            const before = await api('GET', `/config/${section}`);
            expect(before.status(), `GET /config/${section}`).toBe(200);
            const beforeBody = await before.json();
            expect(beforeBody, `${section} GET body empty`).toBeTruthy();

            const put = await api('PUT', `/config/${section}`, sample);
            expect(put.status(), `PUT /config/${section}`).toBe(200);
            const putBody = await put.json();
            expect(putBody, `${section} PUT body empty`).toBeTruthy();

            // wait for fsnotify hot-reload to land in the live snapshot, then
            // re-GET and assert every written field round-tripped.
            const afterBody = await waitForConfig(page, token, section, (b) => {
                return Object.entries(sample).every(([k, v]) => JSON.stringify(b[k]) === JSON.stringify(v));
            }, 10_000);

            for (const [k, v] of Object.entries(sample)) {
                expect(afterBody[k], `${section}.${k} did not round-trip`).toEqual(v);
            }
        });
    }

    test('/config/full GET returns the whole config', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('GET', '/config/full');
        expect(res.status()).toBe(200);
        const body = await res.json();
        expect(body).toBeTruthy();
    });

    test('unauthenticated PUT is rejected (401/403)', async ({ page }) => {
        const res = await page.request.put(`${BASE}/admin/api/config/cors`, {
            data: { allowed_origins: ['*'] },
        });
        expect([401, 403]).toContain(res.status());
    });
});

test.describe('config -> runtime coupling', () => {
    test('CORS: PUT allowed_origins changes the live OPTIONS preflight', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);

        const origin = 'https://runtime-coupling-e2e.example.com';
        const put = await api('PUT', '/config/cors', {
            allowed_origins: [origin],
            allowed_methods: ['GET', 'POST', 'OPTIONS'],
            allowed_headers: ['Content-Type', 'Authorization'],
        });
        expect(put.status()).toBe(200);

        await waitForConfig(page, token, 'cors', (b) =>
            Array.isArray(b.allowed_origins) && b.allowed_origins.includes(origin));

        const pre = await page.request.fetch(`${BASE}/v1/models`, {
            method: 'OPTIONS',
            headers: { Origin: origin, 'Access-Control-Request-Method': 'GET' },
        });
        expect(pre.status(), 'preflight should be 204').toBe(204);
        expect(pre.headers()['access-control-allow-origin'], 'live CORS header must reflect admin config').toBe(origin);
    });

    test('rate-limit: a key with rpm:1 gets 429 on the 2nd request', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);

        await api('PUT', '/config/rate-limit', { enabled: true, key_enforcement: true });
        await waitForConfig(page, token, 'rate-limit', (b) => b.enabled && b.key_enforcement);

        const name = `e2e-rl-${Date.now()}`;
        const created = await api('POST', '/keys', { name, rpm: 1, quota: 0 });
        expect(created.status()).toBe(201);
        const rawKey = (await created.json()).raw_key;
        expect(rawKey).toMatch(/^sk-/);

        const r1 = await page.request.get(`${BASE}/v1/models`, {
            headers: { Authorization: `Bearer ${rawKey}` },
        });
        expect([200, 429], `1st req unexpected: ${r1.status()}`).toContain(r1.status());

        const r2 = await page.request.get(`${BASE}/v1/models`, {
            headers: { Authorization: `Bearer ${rawKey}` },
        });
        expect(r2.status(), `2nd req should be rate-limited (429), got ${r2.status()}`).toBe(429);

        await api('DELETE', `/keys/${name}`).catch(() => {});
    });
});
