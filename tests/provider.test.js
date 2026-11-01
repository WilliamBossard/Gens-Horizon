'use strict';
const test = require('node:test');
const assert = require('node:assert');
const { getProvider, getTokenPath } = require('../provider');

test('provider.js can be imported safely even without config.js', async () => {
    // Calling getProvider without tokens returns null rather than throwing MODULE_NOT_FOUND
    const provider = await getProvider({ provider: 'google' });
    assert.strictEqual(provider, null);
});

test('getTokenPath returns proper path for each provider', async () => {
    const googlePath = await getTokenPath('google');
    assert.ok(googlePath.includes('token_google.json') || googlePath.includes('token.json'));

    const dropboxPath = await getTokenPath('dropbox');
    assert.ok(dropboxPath.includes('token_dropbox.json'));

    const onedrivePath = await getTokenPath('onedrive');
    assert.ok(onedrivePath.includes('token_onedrive.json'));
});

