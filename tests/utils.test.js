'use strict';
const test = require('node:test');
const assert = require('node:assert');
const { parseTargetInstance } = require('../utils');

test('parseTargetInstance parses --instance flag correctly', () => {
    assert.strictEqual(parseTargetInstance(['--sync', '--instance', 'MyModpack']), 'MyModpack');
    assert.strictEqual(parseTargetInstance(['--upload', '--instance=MyModpack']), 'MyModpack');
});

test('parseTargetInstance does not mistake flags or providers for instance name', () => {
    // When flags with arguments are passed:
    assert.strictEqual(parseTargetInstance(['--sync', '--provider', 'google']), null);
    assert.strictEqual(parseTargetInstance(['--upload', '--provider', 'dropbox']), null);
    assert.strictEqual(parseTargetInstance(['--sync', '--provider', 'onedrive', '--force']), null);
    assert.strictEqual(parseTargetInstance(['--sync', '--lang', 'fr']), null);
    
    // When a target instance is supplied along with --provider:
    assert.strictEqual(parseTargetInstance(['--sync', '--provider', 'google', 'MyModpack']), 'MyModpack');
    assert.strictEqual(parseTargetInstance(['--upload', 'MyModpack', '--provider', 'dropbox']), 'MyModpack');
});

test('parseTargetInstance ignores known commands and providers', () => {
    assert.strictEqual(parseTargetInstance(['--sync', 'google']), null);
    assert.strictEqual(parseTargetInstance(['sync']), null);
    assert.strictEqual(parseTargetInstance(['upload']), null);
    assert.strictEqual(parseTargetInstance(['delete']), null);
    assert.strictEqual(parseTargetInstance(['--sync', 'ValidInstanceName']), 'ValidInstanceName');
});

