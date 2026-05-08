'use strict';

const fs   = require('fs');
const path = require('path');

const LOCK_FILE = path.join(process.cwd(), 'horizon.lock');
function acquireLock() {
    if (fs.existsSync(LOCK_FILE)) {
        const raw = fs.readFileSync(LOCK_FILE, 'utf8').trim();
        const pid = parseInt(raw, 10);

        if (!isNaN(pid) && pid !== process.pid) {
            try {
                process.kill(pid, 0);
                return false;
            } catch (_) {
                process.stderr.write(`[lock] Verrou périmé (PID ${pid} mort) — nettoyage.\n`);
            }
        }
    }

    fs.writeFileSync(LOCK_FILE, String(process.pid), 'utf8');

    const release = () => releaseLock();
    process.on('exit',   release);
    process.on('SIGTERM', () => { release(); process.exit(0); });
    process.on('SIGINT',  () => { release(); process.exit(0); });

    return true;
}

function releaseLock() {
    try {
        if (!fs.existsSync(LOCK_FILE)) return;
        const pid = parseInt(fs.readFileSync(LOCK_FILE, 'utf8').trim(), 10);
        if (pid === process.pid) fs.unlinkSync(LOCK_FILE);
    } catch (_) {}
}

module.exports = { acquireLock, releaseLock };
