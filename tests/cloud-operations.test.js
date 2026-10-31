'use strict';
const test = require('node:test');
const assert = require('node:assert');
const { getCloudIndexAndCleanDuplicates } = require('../cloud-operations');
const { getCanonicalName } = require('../utils');

test('getCanonicalName cleans invalid characters and falls back on empty', () => {
    assert.strictEqual(getCanonicalName('My Instance!'), 'My Instance!');
    assert.strictEqual(getCanonicalName('My/Bad:Name?*'), 'MyBadName');
    assert.strictEqual(getCanonicalName('???'), 'UnknownInstance');
    assert.strictEqual(getCanonicalName(''), 'UnknownInstance');
    assert.strictEqual(getCanonicalName(null), 'UnknownInstance');
});

test('getCloudIndexAndCleanDuplicates keeps newest file and deletes older', async () => {
    const deleted = [];
    const mockProvider = {
        listFiles: async () => [
            { id: '1', name: 'GensHorizon_Backup_Test.zip', modifiedTime: '2026-10-31T10:00:00Z', size: 100 },
            { id: '2', name: 'GensHorizon_Backup_Test.zip', modifiedTime: '2026-10-31T12:00:00Z', size: 120 }
        ],
        deleteFile: async (id) => {
            deleted.push(id);
        }
    };

    const index = await getCloudIndexAndCleanDuplicates(mockProvider, { maxRetries: 1, baseDelay: 10 }, '[test]');
    
    // Should keep the one modified at 12:00:00 (id 2)
    assert.strictEqual(index['GensHorizon_Backup_Test.zip'].id, '2');
    // Should have deleted the older one (id 1)
    assert.deepStrictEqual(deleted, ['1']);
});

