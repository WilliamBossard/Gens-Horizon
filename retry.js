'use strict';
const RETRYABLE_CODES = new Set([
    'ECONNRESET', 'ECONNREFUSED', 'ETIMEDOUT',
    'ENOTFOUND',  'EAI_AGAIN',    'EPIPE',
    'ECONNABORTED', 'EHOSTUNREACH',
]);
const RETRYABLE_STATUS = new Set([429, 500, 502, 503, 504]);
function isRetryable(err) {
    if (!err) return false;
    if (RETRYABLE_CODES.has(err.code))        return true;
    if (RETRYABLE_STATUS.has(err.statusCode)) return true;
    const msg = (err.message || '').toLowerCase();
    return msg.includes('timeout')
        || msg.includes('socket hang up')
        || msg.includes('network error')
        || msg.includes('econnreset');
}
async function withRetry(fn, { maxRetries = 3, baseDelay = 1500, label = 'opération' } = {}) {
    for (let attempt = 1; attempt <= maxRetries + 1; attempt++) {
        try {
            return await fn();
        } catch (err) {
            const isLast = attempt > maxRetries;
            if (isLast || !isRetryable(err)) throw err;
            const cap   = 30_000;
            const delay = Math.random() * Math.min(cap, baseDelay * Math.pow(2, attempt - 1));
            process.stderr.write(
                `[retry] "${label}" échoué (tentative ${attempt}/${maxRetries}) : ${err.message}` +
                ` — nouvel essai dans ${Math.round(delay)}ms\n`
            );
            await new Promise(r => setTimeout(r, delay));
        }
    }
}
module.exports = { withRetry, isRetryable };