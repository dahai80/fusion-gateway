// Global setup for the admin E2E suite.
//
// Runs exactly ONCE per `playwright test` invocation, outside all workers.
// Backs up the user's config.yaml to a stable path under tests/e2e so the
// mutating config-PUT tests can never leave the live gateway in a changed
// state. global-teardown.js restores it. This replaces the per-file
// beforeAll/afterAll backup which raced across spec files and left config
// polluted.

const fs = require('fs');
const path = require('path');

const CFG = process.env.FG_CONFIG || path.resolve(__dirname, '../../config.yaml');
const BAK = path.resolve(__dirname, '.config-backup.yaml');

async function globalSetup() {
    if (!fs.existsSync(CFG)) {
        throw new Error(`config.yaml not found at ${CFG} — set FG_CONFIG to the gateway config path`);
    }
    fs.copyFileSync(CFG, BAK);
    console.log(`[global-setup] config backed up: ${CFG} -> ${BAK}`);
}

module.exports = globalSetup;
module.exports.CFG = CFG;
module.exports.BAK = BAK;
