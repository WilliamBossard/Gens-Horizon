const test = require('node:test');
const assert = require('node:assert');
const { generateManifest, compareManifests } = require('../scanner');
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
