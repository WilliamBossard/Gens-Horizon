'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');

test('lock acquire and release works and cleans up properly', async () => {
    const tmpDataDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-lock-test-'));
    const oldHorizonDataDir = process.env.HORIZON_DATA_DIR;
    process.env.HORIZON_DATA_DIR = tmpDataDir;

    // Clear module cache to re-initialize with new HORIZON_DATA_DIR
    const lockPath = require.resolve('../lock');
    delete require.cache[lockPath];
    const { acquireLock, releaseLock, LOCK_FILE } = require('../lock');

    try {
        const acquired = await acquireLock();
        assert.strictEqual(acquired, true);
        assert.strictEqual(fs.existsSync(LOCK_FILE), true);

        const content = fs.readFileSync(LOCK_FILE, 'utf8').trim();
        assert.strictEqual(content, String(process.pid));

        releaseLock();
        assert.strictEqual(fs.existsSync(LOCK_FILE), false);
    } finally {
        releaseLock();
        if (oldHorizonDataDir !== undefined) process.env.HORIZON_DATA_DIR = oldHorizonDataDir;
        else delete process.env.HORIZON_DATA_DIR;
        delete require.cache[lockPath];
        await fs.promises.rm(tmpDataDir, { recursive: true, force: true });
    }
});

test('stale lock with dead PID is cleaned up and reacquired', async () => {
    const tmpDataDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-lock-stale-'));
    const oldHorizonDataDir = process.env.HORIZON_DATA_DIR;
    process.env.HORIZON_DATA_DIR = tmpDataDir;

    const lockPath = require.resolve('../lock');
    delete require.cache[lockPath];
    const { acquireLock, releaseLock, LOCK_FILE } = require('../lock');

    try {
        // Write an impossible/dead PID (e.g., 99999999)
        const deadPid = 99999999;
        await fs.promises.writeFile(LOCK_FILE, String(deadPid));

        // acquireLock should recognize dead PID, clean it up, and acquire lock
        const acquired = await acquireLock();
        assert.strictEqual(acquired, true);

        const content = fs.readFileSync(LOCK_FILE, 'utf8').trim();
        assert.strictEqual(content, String(process.pid));

        releaseLock();
    } finally {
        releaseLock();
        if (oldHorizonDataDir !== undefined) process.env.HORIZON_DATA_DIR = oldHorizonDataDir;
        else delete process.env.HORIZON_DATA_DIR;
        delete require.cache[lockPath];
        await fs.promises.rm(tmpDataDir, { recursive: true, force: true });
    }
});

