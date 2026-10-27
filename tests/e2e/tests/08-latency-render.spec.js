// Latency page render regression.
//
// The anomaly overlay draws recharts ReferenceLine elements on charts whose
// Y axes are named left/right. ReferenceLine defaults to yAxis id 0, which
// does not exist there, so recharts throws "Invariant failed" and React
// unmounts the ENTIRE page — both charts vanish whenever at least one
// detected anomaly falls inside the selected window. The API was fine; the
// page crashed client-side (#360).
//
// This spec pins the fixed behavior:
//   1. no "Invariant failed" console error,
//   2. both chart cards render with their line curves,
//   3. the anomaly overlay (when present) does not kill the page.

const { test, expect, login, openAsAdmin } = require('../fixtures');

test('latency page renders both charts without recharts invariant crash', async ({ page }) => {
    const errors = [];
    page.on('console', (m) => {
        if (m.type() === 'error') errors.push(m.text());
    });
    page.on('pageerror', (e) => errors.push(e.message));

    const { token } = await login(page);
    await openAsAdmin(page, token, '/admin/insights/latency');

    await expect(
        page.getByRole('heading', { name: 'Latency (TTFT / TPOT)' })
    ).toBeVisible({ timeout: 15_000 });

    const invariants = errors.filter((t) => t.includes('Invariant failed'));
    expect(invariants, `recharts invariant thrown: ${invariants[0] || ''}`).toHaveLength(0);

    // Empty-window alert is legitimate on a gateway with no stream traffic;
    // when the window has data, both cards must show their lines.
    const emptyAlert = await page
        .getByText('No generation traffic in this window', { exact: false })
        .count();
    if (emptyAlert === 0) {
        await expect(
            page.locator('.ant-card-head-title').filter({ hasText: 'Time to First Token (ms)' })
        ).toBeVisible();
        await expect(
            page.locator('.ant-card-head-title').filter({ hasText: 'Time Per Output Token (ms/token)' })
        ).toBeVisible();
        const curves = await page.locator('.recharts-line-curve').count();
        expect(curves, 'expected 6 percentile lines across the two charts').toBeGreaterThanOrEqual(6);
    }

    // Any remaining console error is worth surfacing, but only the recharts
    // invariant is fatal for this page — do not fail on unrelated noise.
});
