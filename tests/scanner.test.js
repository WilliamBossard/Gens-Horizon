const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { generateManifest, compareManifests, IGNORED } = require('../scanner');
const { withConcurrency } = require('../utils');

test('compareManifests detects changes', () => {
    const oldM = {
        'a.txt': { hash: '123' },
        'b.txt': { hash: '456' }
    };
    const newM = {
        'a.txt': { hash: '123' },
        'b.txt': { hash: '789' },
        'c.txt': { hash: 'abc' }
    };

    const res = compareManifests(oldM, newM);
    assert.strictEqual(res.hasChanges, true);
    assert.deepStrictEqual(res.added, ['c.txt']);
    assert.deepStrictEqual(res.modified, ['b.txt']);
    assert.deepStrictEqual(res.deleted, []);
});

test('compareManifests detects deletions', () => {
    const oldM = { 'a.txt': { hash: '123' } };
    const newM = {};

    const res = compareManifests(oldM, newM);
    assert.strictEqual(res.hasChanges, true);
    assert.deepStrictEqual(res.deleted, ['a.txt']);
});

test('withConcurrency limits correctly', async () => {
    let running = 0;
    let maxRunning = 0;

    const tasks = Array.from({ length: 10 }).map(() => async () => {
        running++;
        if (running > maxRunning) maxRunning = running;
        await new Promise(r => setTimeout(r, 10));
        running--;
    });

    await withConcurrency(3, tasks);
    assert.strictEqual(maxRunning, 3);
});

test('scanner exports IGNORED and ignores specified directories in generateManifest', async () => {
    assert.ok(IGNORED instanceof Set);
    assert.ok(IGNORED.has('logs'));
    assert.ok(IGNORED.has('assets'));
    assert.ok(IGNORED.has('backups'));
    assert.ok(IGNORED.has('screenshots'));
    assert.ok(IGNORED.has('session.lock'));

    const tmpDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-scanner-test-'));
    try {
        await fs.promises.writeFile(path.join(tmpDir, 'options.txt'), 'gamma:1.0');
        await fs.promises.mkdir(path.join(tmpDir, 'saves'), { recursive: true });
        await fs.promises.writeFile(path.join(tmpDir, 'saves', 'level.dat'), 'data');

        // Create ignored directories
        await fs.promises.mkdir(path.join(tmpDir, 'logs'), { recursive: true });
        await fs.promises.writeFile(path.join(tmpDir, 'logs', 'latest.log'), 'log content');
        await fs.promises.mkdir(path.join(tmpDir, 'screenshots'), { recursive: true });
        await fs.promises.writeFile(path.join(tmpDir, 'screenshots', 'shot1.png'), 'png data');

        const manifest = await generateManifest(tmpDir);

        assert.ok('options.txt' in manifest);
        assert.ok('saves/level.dat' in manifest);
        assert.ok(!('logs/latest.log' in manifest));
        assert.ok(!('screenshots/shot1.png' in manifest));
    } finally {
        await fs.promises.rm(tmpDir, { recursive: true, force: true });
    }
});

test('generateManifest retains oldManifest entry on transient non-ENOENT error', async () => {
    const tmpDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-scanner-preserve-'));
    const origStat = fs.promises.stat;
    try {
        const filePath = path.join(tmpDir, 'locked.txt');
        await fs.promises.writeFile(filePath, 'important data');
        const oldM = {
            'locked.txt': { hash: 'hash123', mtime: 1000, size: 14 }
        };

        // Mock stat to simulate EBUSY for locked.txt
        fs.promises.stat = async (p, ...args) => {
            if (p.includes('locked.txt')) {
                const err = new Error('resource busy or locked');
                err.code = 'EBUSY';
                throw err;
            }
            return origStat.call(fs.promises, p, ...args);
        };

        const manifest = await generateManifest(tmpDir, tmpDir, oldM);
        assert.strictEqual(manifest['locked.txt'].hash, 'hash123');

        // Verify compareManifests does not consider it deleted
        const diff = compareManifests(oldM, manifest);
        assert.strictEqual(diff.deleted.length, 0);
    } finally {
        fs.promises.stat = origStat;
        await fs.promises.rm(tmpDir, { recursive: true, force: true });
    }
});
