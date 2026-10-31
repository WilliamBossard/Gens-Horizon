'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { scanInstances, getProviderName } = require('../paths');

test('scanInstances ignores _rollback_ directories and requires instance.json', async () => {
    // We can point HORIZON_DATA_DIR / APPDATA to a temporary directory
    const tmpRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-paths-test-'));
    const oldAppData = process.env.APPDATA;
    const oldUserProfile = process.env.USERPROFILE;
    const oldHome = process.env.HOME;

    try {
        process.env.APPDATA = tmpRoot;
        process.env.USERPROFILE = tmpRoot;
        process.env.HOME = tmpRoot;

        const instancesDir = path.join(tmpRoot, 'GensLauncher', 'instances');
        await fs.promises.mkdir(instancesDir, { recursive: true });

        // Normal instance with instance.json
        await fs.promises.mkdir(path.join(instancesDir, 'ValidInstance'), { recursive: true });
        await fs.promises.writeFile(path.join(instancesDir, 'ValidInstance', 'instance.json'), '{}');

        // Rollback folder with instance.json (MUST be ignored)
        await fs.promises.mkdir(path.join(instancesDir, 'ValidInstance_rollback_123456789'), { recursive: true });
        await fs.promises.writeFile(path.join(instancesDir, 'ValidInstance_rollback_123456789', 'instance.json'), '{}');

        // Folder without instance.json (MUST be ignored)
        await fs.promises.mkdir(path.join(instancesDir, 'IncompleteInstance'), { recursive: true });

        const instances = await scanInstances();
        assert.deepStrictEqual(instances, ['ValidInstance']);
    } finally {
        if (oldAppData !== undefined) process.env.APPDATA = oldAppData;
        else delete process.env.APPDATA;
        if (oldUserProfile !== undefined) process.env.USERPROFILE = oldUserProfile;
        else delete process.env.USERPROFILE;
        if (oldHome !== undefined) process.env.HOME = oldHome;
        else delete process.env.HOME;
        await fs.promises.rm(tmpRoot, { recursive: true, force: true });
    }
});

test('getProviderName falls back to settings or default', () => {
    assert.strictEqual(getProviderName({ provider: 'dropbox' }), 'dropbox');
    assert.strictEqual(getProviderName({ provider: 'onedrive' }), 'onedrive');
    assert.strictEqual(getProviderName({}), 'google');
    assert.strictEqual(getProviderName(null), 'google');

    // Test positional provider argument
    process.argv.push('dropbox');
    assert.strictEqual(getProviderName({ provider: 'google' }), 'dropbox');
    process.argv.pop();

    // Test --provider flag
    process.argv.push('--provider', 'onedrive');
    assert.strictEqual(getProviderName({ provider: 'google' }), 'onedrive');
    process.argv.pop();
    process.argv.pop();
});

