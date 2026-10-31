'use strict';
const RETRYABLE_CODES = new Set([
    'ECONNRESET', 'ECONNREFUSED', 'ETIMEDOUT',
    'ENOTFOUND',  'EAI_AGAIN',    'EPIPE',
    'ECONNABORTED', 'EHOSTUNREACH',
]);
const RETRYABLE_STATUS = new Set([429, 500, 502, 503, 504]);
function isRetryable(err) {
    if (!err) return false;
    const code = err.code || err.cause?.code;
    if (code && RETRYABLE_CODES.has(code)) return true;
    const status = err.statusCode || err.status || err.response?.status || err.cause?.statusCode || err.cause?.status;
    if (status && RETRYABLE_STATUS.has(status)) return true;
    if (status === 401 || status === 403) return false;
    const msg = (err.message || '').toLowerCase();
    const causeMsg = (err.cause?.message || '').toLowerCase();
    return msg.includes('timeout')
        || msg.includes('socket hang up')
        || msg.includes('network error')
        || msg.includes('fetch failed')
        || msg.includes('econnreset')
        || causeMsg.includes('timeout')
        || causeMsg.includes('socket hang up')
        || causeMsg.includes('network error')
        || causeMsg.includes('econnreset');
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