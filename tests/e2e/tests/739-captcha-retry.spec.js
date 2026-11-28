const { test, expect } = require('@playwright/test');

const SVG_CAPTCHA = {
    enabled: true,
    captcha_id: 'e2e-739',
    image: 'data:image/svg+xml;base64,' + Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="120" height="48"><text x="4" y="30">739E</text></svg>').toString('base64'),
    ttl_seconds: 120
};

test('#739 captcha fetch retry once then visible click-to-retry error state', async ({ page }) => {
    let fails = 2;
    await page.route('**/auth/api/captcha', async (route) => {
        if (fails > 0) { fails--; return route.fulfill({ status: 502, contentType: 'text/html', body: '<html>bad gateway</html>' }); }
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(SVG_CAPTCHA) });
    });
    await page.goto('http://127.0.0.1:11432/auth/');
    const err = page.locator('#captcha-error');
    await expect(page.locator('#captcha-row')).toBeVisible({ timeout: 5000 });
    await expect(err).toBeVisible({ timeout: 5000 });
    await expect(page.locator('#captcha-line')).toBeHidden();
    await expect(page.locator('#login-captcha')).toBeHidden();
    await err.click();
    await expect(page.locator('#captcha-img')).toHaveAttribute('src', /data:image/, { timeout: 5000 });
    await expect(err).toBeHidden();
    await expect(page.locator('#captcha-line')).toBeVisible();
    await expect(page.locator('#login-captcha')).toBeVisible();
});
