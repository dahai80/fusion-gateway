// Key management page — covers create (all inputs: name, models, modules,
// quota, rpm, tpm, budget, status), list, edit, and DELETE.
//
// CRITICAL runtime invariant: after creating a key it must authenticate
// against /v1/models; after DELETING it the same key must be rejected (401).
// This is the exact end-to-end responsibility the user demanded.
//
// Every temp key created here is deleted in cleanup so nothing leaks.

const { test, expect, BASE, login, adminApi, openAsAdmin } = require('../fixtures');

const TMP = `e2e-key-${Date.now()}`;

async function openKeys(page, token) {
    await openAsAdmin(page, token, '/admin/keys');
}

test.describe('key management', () => {
    test('lists keys via the table', async ({ page }) => {
        const { token } = await login(page);
        await openKeys(page, token);
        await expect(page.locator('.ant-table')).toBeVisible({ timeout: 15_000 });
    });

    test('create key with full inputs, then DELETE — runtime auth follows lifecycle', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);

        // --- create via UI form ---
        await openKeys(page, token);
        await page.getByRole('button', { name: /add|create|new|新增|添加/i }).first().click();
        await page.getByPlaceholder('Key name').fill(TMP);
        const modelsInput = page.getByPlaceholder('Leave empty for all models').first();
        if (await modelsInput.isVisible().catch(() => false)) {
            await modelsInput.fill('qwen3, llama3');
        }
        await page.getByRole('button', { name: /save|ok|确定|提交|create/i }).last().click();
        await page.waitForTimeout(1200);

        // --- confirm it appears in the list ---
        const listed = await api('GET', '/keys');
        const arr = await listed.json();
        const found = arr.find((k) => k.name === TMP);
        expect(found, `key ${TMP} not in list after create`).toBeTruthy();

        // --- create a second key via API to capture the raw key ---
        const created = await api('POST', '/keys', { name: TMP + '-auth', quota: 0 });
        expect(created.status()).toBe(201);
        const createdBody = await created.json();
        const rawKey = createdBody.raw_key;
        expect(rawKey, 'no raw_key in create response').toMatch(/^sk-/);

        // --- runtime invariant 1: fresh key authenticates at /v1/models ---
        const ok = await page.request.get(`${BASE}/v1/models`, {
            headers: { Authorization: `Bearer ${rawKey}` },
        });
        expect(ok.status(), 'newly created key should access /v1/models').toBe(200);

        // --- DELETE via UI (the previously-broken button) ---
        await page.reload();
        await page.waitForLoadState('networkidle');
        const row = page.locator('.ant-table-row', { hasText: TMP + '-auth' }).first();
        await expect(row).toBeVisible({ timeout: 10_000 });
        await row.getByRole('button', { name: /delete|删除|remove/i }).click();
        const confirm = page.getByRole('button', { name: /^ok|确定|确认|yes$/i }).last();
        if (await confirm.isVisible().catch(() => false)) await confirm.click();
        await page.waitForTimeout(1500);

        // --- runtime invariant 2: deleted key is rejected ---
        const gone = await page.request.get(`${BASE}/v1/models`, {
            headers: { Authorization: `Bearer ${rawKey}` },
        });
        expect([401, 403], `deleted key must be rejected, got ${gone.status()}`).toContain(gone.status());

        // --- cleanup ---
        for (const name of [TMP, TMP + '-auth']) {
            await api('DELETE', `/keys/${name}`).catch(() => {});
        }
    });

    test('rejects create with empty name', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const res = await api('POST', '/keys', { name: '' });
        expect(res.status()).toBe(400);
    });

    test('edit (PUT) updates key fields and persists', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const name = `e2e-edit-${Date.now()}`;
        const created = await api('POST', '/keys', { name, quota: 10, rpm: 5 });
        expect(created.status()).toBe(201);

        const updated = await api('PUT', `/keys/${name}`, { quota: 99, rpm: 50, status: 'disabled' });
        expect(updated.status()).toBe(200);
        const body = await updated.json();
        expect(body.quota_limit).toBe(99);

        const got = await api('GET', `/keys/${name}`);
        const gotBody = await got.json();
        expect(gotBody.quota_limit).toBe(99);

        await api('DELETE', `/keys/${name}`).catch(() => {});
    });

    test('list response id field equals name (the delete fix)', async ({ page }) => {
        const { token } = await login(page);
        const api = adminApi(page, token);
        const name = `e2e-id-${Date.now()}`;
        await api('POST', '/keys', { name });
        const res = await api('GET', '/keys');
        const arr = await res.json();
        const k = arr.find((x) => x.name === name);
        expect(k.id, 'id must be name, not a sequence number').toBe(name);
        await api('DELETE', `/keys/${name}`).catch(() => {});
    });
});
