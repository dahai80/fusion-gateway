// Channel management page — create/list/edit/delete with all inputs:
// name, type, base_url, key, models, priority, weight, status.

const { test, expect, BASE, login, adminApi, openAsAdmin } = require('../fixtures');

async function openChannels(page, token) {
    await openAsAdmin(page, token, '/admin/channels');
}

// Issue #241: a failed test skips its own cleanup, and residue channels from
// a failed run push fresh rows past the table's first page (20/page), making
// the next full-suite run's row-visibility asserts flaky. Sweep every
// e2e-ch* channel after the suite (covers e2e-chedit-*/e2e-chid-*); channel
// DELETE is a hard delete, unlike keys (F8 soft-delete).
const CH_PREFIX = 'e2e-ch';

async function pollUntil(fn, ms = 5000, step = 250) {
    const start = Date.now();
    for (;;) {
        const v = await fn();
        if (v) return v;
        if (Date.now() - start > ms) return null;
        await new Promise((r) => setTimeout(r, step));
    }
}

test.describe('channel management', () => {
    test.afterAll(async ({ request }) => {
        const creds = {
            username: process.env.FG_ADMIN_USER || 'admin',
            password: process.env.FG_ADMIN_PASS || 'change-me-secure-password',
        };
        const loginRes = await request.post(`${BASE}/admin/api/login`, { data: creds }).catch(() => null);
        if (!loginRes || !loginRes.ok()) return;
        const { token } = await loginRes.json();
        const res = await request.get(`${BASE}/admin/api/channels`, {
            headers: { Authorization: `Bearer ${token}` },
        });
        if (!res.ok()) return;
        const arr = await res.json();
        for (const c of arr) {
            if (String(c.name).startsWith(CH_PREFIX)) {
                await request.fetch(`${BASE}/admin/api/channels/${c.name}`, {
                    method: 'DELETE',
                    headers: { Authorization: `Bearer ${token}` },
                }).catch(() => {});
            }
        }
    });

    test('lists channels via the table', async ({ page }) => {
        const { token } = await login(page);
        await openChannels(page, token);
        await expect(page.locator('.ant-table')).toBeVisible({ timeout: 15_000 });
    });

    test('create channel with inputs, then DELETE', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const name = `e2e-ch-${Date.now()}`;

        await openChannels(page, token);
        await page.getByRole('button', { name: /add|create|new|新增|添加/i }).first().click();
        // Deterministic modal wait — fixed sleeps were the flake source (#241).
        await expect(page.locator('#name')).toBeVisible({ timeout: 10_000 });
        await page.locator('#name').fill(name);
        // type is an antd Select; click the selector and pick an option
        await page.locator('#type').click();
        // Scope to the OPEN dropdown (antd keeps hidden dropdowns in the DOM)
        // and match case-insensitively — option labels are like "OpenAI".
        await page.locator('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option', { hasText: /openai/i }).first().click();
        // Selected value renders in the sibling .ant-select-selection-item,
        // not the readonly input itself.
        await expect(page.locator('.ant-select:has(#type) .ant-select-selection-item')).toHaveText(/openai/i);
        await page.locator('#base_url').fill('https://api.example.com/v1');
        await page.locator('#priority').fill('1');
        await page.locator('#weight').fill('5');
        await page.getByRole('button', { name: /save|ok|确定|提交|create|add/i }).last().click();
        // Confirm create via API polling, not a fixed sleep.
        const found = await pollUntil(async () => {
            const res = await api('GET', '/channels');
            const arr = await res.json();
            return arr.find((c) => c.name === name) || null;
        });
        expect(found, `channel ${name} not in list after create`).toBeTruthy();

        // DELETE via UI
        await page.reload();
        await page.waitForLoadState('networkidle');
        // Table paginates at 20/page; residue can push the fresh row past
        // page 1 (see #241). Switch to 100/page before asserting visibility.
        await page.locator('.ant-pagination .ant-select').click();
        await page.getByRole('option', { name: '100 / page' }).click();
        const row = page.locator('.ant-table-row', { hasText: name }).first();
        await expect(row).toBeVisible({ timeout: 10_000 });
        await row.getByRole('button', { name: /delete|删除|remove/i }).click();
        const confirm = page.getByRole('button', { name: /^ok|确定|确认|yes$/i }).last();
        if (await confirm.isVisible().catch(() => false)) await confirm.click();
        // Confirm delete via API polling.
        const gone = await pollUntil(async () => {
            const res = await api('GET', '/channels');
            const arr = await res.json();
            return arr.find((c) => c.name === name) ? null : true;
        });
        expect(gone, 'channel should be gone after delete').toBeTruthy();
    });

    test('rejects create with empty name', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('POST', '/channels', { name: '', type: 'openai' });
        expect(res.status()).toBe(400);
    });

    test('edit (PUT) updates channel and persists', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const name = `e2e-chedit-${Date.now()}`;
        await api('POST', '/channels', { name, type: 'openai', base_url: 'https://a/v1', weight: 1 });
        const updated = await api('PUT', `/channels/${name}`, { type: 'anthropic', base_url: 'https://b/v1', weight: 9 });
        expect(updated.status()).toBe(200);
        const got = await api('GET', `/channels/${name}`);
        const body = await got.json();
        expect(body.type).toBe('anthropic');
        expect(body.weight).toBe(9);
        await api('DELETE', `/channels/${name}`).catch(() => {});
    });

    test('list response id field equals name (the delete fix)', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const name = `e2e-chid-${Date.now()}`;
        await api('POST', '/channels', { name, type: 'openai' });
        const res = await api('GET', '/channels');
        const arr = await res.json();
        const c = arr.find((x) => x.name === name);
        expect(c.id, 'id must be name, not a sequence number').toBe(name);
        await api('DELETE', `/channels/${name}`).catch(() => {});
    });
});
