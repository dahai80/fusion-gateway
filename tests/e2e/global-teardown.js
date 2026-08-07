// Global teardown for the admin E2E suite.
//
// Runs exactly ONCE after all tests, outside all workers. Restores the
// user's config.yaml from the snapshot taken in global-setup.js, then waits
// for the gateway's fsnotify hot-reload to pick up the restored file so the
// live gateway is left in its original state.

const fs = require('fs');
const path = require('path');

const CFG = process.env.FG_CONFIG || path.resolve(__dirname, '../../config.yaml');
const BAK = path.resolve(__dirname, '.config-backup.yaml');

async function globalTeardown() {
    if (fs.existsSync(BAK)) {
        fs.copyFileSync(BAK, CFG);
        fs.unlinkSync(BAK);
        console.log(`[global-teardown] config restored: ${BAK} -> ${CFG}`);
        await new Promise((r) => setTimeout(r, 1500)); // let fsnotify reload
    } else {
        console.warn(`[global-teardown] no backup at ${BAK} — config not restored`);
    }
}

module.exports = globalTeardown;
