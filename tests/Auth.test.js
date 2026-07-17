process.env.NODE_ENV = 'test';
const test = require('node:test');
const assert = require('node:assert');
const { _encrypt, _decrypt } = require('../Auth');
const crypto = require('crypto');

test('encryption and decryption are symmetrical', async () => {
    const originalText = JSON.stringify({ access_token: '123', refresh_token: 'abc' });

    const encrypted = await _encrypt(originalText);
    const { decrypted } = await _decrypt(encrypted);

    assert.strictEqual(decrypted, originalText);
});

test('decrypt fails on corrupted data', async () => {
    try {
        await _decrypt('aes:abcd1234abcd1234abcd1234abcd1234:invalid_hex_data');
        assert.fail('Should have thrown an error');
    } catch (err) {
        assert.ok(err);
    }
});
