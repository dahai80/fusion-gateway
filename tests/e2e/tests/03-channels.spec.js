// Channel management page — create/list/edit/delete with all inputs:
// name, type, base_url, key, models, priority, weight, status.

const { test, expect, BASE, login, adminApi, openAsAdmin } = require('../fixtures');

async function openChannels(page, token) {
    await openAsAdmin(page, token, '/admin/channels');
}

test.describe('channel management', () => {
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
        await page.waitForTimeout(800);
        await page.locator('#name').fill(name);
        // type is an antd Select; click the selector and pick an option
        await page.locator('#type').click();
        await page.waitForTimeout(400);
        await page.keyboard.type('openai');
        await page.keyboard.press('Enter');
        await page.locator('#base_url').fill('https://api.example.com/v1');
        await page.locator('#priority').fill('1');
        await page.locator('#weight').fill('5');
        await page.getByRole('button', { name: /save|ok|确定|提交|create|add/i }).last().click();
        await page.waitForTimeout(1200);

        const res = await api('GET', '/channels');
        const arr = await res.json();
        const found = arr.find((c) => c.name === name);
        expect(found, `channel ${name} not in list after create`).toBeTruthy();

        // DELETE via UI
        await page.reload();
        await page.waitForLoadState('networkidle');
        const row = page.locator('.ant-table-row', { hasText: name }).first();
        await expect(row).toBeVisible({ timeout: 10_000 });
        await row.getByRole('button', { name: /delete|删除|remove/i }).click();
        const confirm = page.getByRole('button', { name: /^ok|确定|确认|yes$/i }).last();
        if (await confirm.isVisible().catch(() => false)) await confirm.click();
        await page.waitForTimeout(1200);

        const after = await api('GET', '/channels');
        const afterArr = await after.json();
        expect(afterArr.find((c) => c.name === name), 'channel should be gone after delete').toBeFalsy();
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
