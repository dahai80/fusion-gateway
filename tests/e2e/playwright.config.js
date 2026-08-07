// Playwright config for Fusion-Gateway admin dashboard E2E tests.
// Targets the live gateway at http://127.0.0.1:11432 (started separately).
// Tests back up config.yaml before mutating config and restore it afterward
// so the running gateway is left untouched.

const { defineConfig, devices } = require('@playwright/test');

const BASE = process.env.FG_BASE || 'http://127.0.0.1:11432';

module.exports = defineConfig({
    testDir: './tests',
    timeout: 60_000,
    expect: { timeout: 15_000 },
    fullyParallel: false,          // admin API mutates shared config; serialize
    retries: 0,
    workers: 1,
    globalSetup: require.resolve('./global-setup.js'),
    globalTeardown: require.resolve('./global-teardown.js'),
    reporter: [['list'], ['html', { open: 'never' }]],
    use: {
        baseURL: BASE,
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        video: 'retain-on-failure',
        actionTimeout: 15_000,
        navigationTimeout: 20_000,
    },
    projects: [
        {
            name: 'chromium',
            use: { ...devices['Desktop Chrome'], channel: undefined },
        },
    ],
});
