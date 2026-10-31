const test = require('node:test');
const assert = require('node:assert');
const { withRetry, isRetryable } = require('../retry');

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

test('isRetryable identifies retryable conditions including err.cause and status', () => {
    assert.strictEqual(isRetryable(null), false);
    
    // Direct error code
    const errConn = new Error('network drop');
    errConn.code = 'ECONNRESET';
    assert.strictEqual(isRetryable(errConn), true);
    
    // Nested cause error code (like native fetch)
    const fetchErr = new Error('fetch failed');
    fetchErr.cause = { code: 'ENOTFOUND' };
    assert.strictEqual(isRetryable(fetchErr), true);
    
    // Status codes
    const err429 = new Error('Rate limit');
    err429.statusCode = 429;
    assert.strictEqual(isRetryable(err429), true);
    
    const err503 = new Error('Service Unavailable');
    err503.status = 503;
    assert.strictEqual(isRetryable(err503), true);
    
    // Non-retryable
    const err401 = new Error('Unauthorized');
    err401.statusCode = 401;
    assert.strictEqual(isRetryable(err401), false);
    
    const err403 = new Error('Forbidden');
    err403.statusCode = 403;
    assert.strictEqual(isRetryable(err403), false);
});
