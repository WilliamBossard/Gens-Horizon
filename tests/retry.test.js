const test = require('node:test');
const assert = require('node:assert');
const { withRetry } = require('../retry');

test('withRetry succeeds on first try', async () => {
    let attempts = 0;
    const fn = async () => { attempts++; return 'success'; };

    const res = await withRetry(fn, { maxRetries: 3, baseDelay: 10 });
    assert.strictEqual(res, 'success');
    assert.strictEqual(attempts, 1);
});

test('withRetry succeeds after failures', async () => {
    let attempts = 0;
    const fn = async () => {
        attempts++;
        if (attempts < 3) {
            const err = new Error('fail');
            err.code = 'ECONNRESET';
            throw err;
        }
        return 'success';
    };

    const res = await withRetry(fn, { maxRetries: 3, baseDelay: 10 });
    assert.strictEqual(res, 'success');
    assert.strictEqual(attempts, 3);
});

test('withRetry fails after max retries', async () => {
    let attempts = 0;
    const fn = async () => {
        attempts++;
        const err = new Error('fail');
        err.code = 'ECONNRESET';
        throw err;
    };

    try {
        await withRetry(fn, { maxRetries: 2, baseDelay: 10 });
        assert.fail('Should have thrown');
    } catch (err) {
        assert.strictEqual(err.message, 'fail');
        assert.strictEqual(attempts, 3);
    }
});
