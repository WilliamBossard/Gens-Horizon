'use strict';

const fs   = require('fs');
const path = require('path');

const LOCK_FILE = path.join(process.cwd(), 'horizon.lock');
const MAX_LOCK_RETRIES = 5;

function acquireLock(attempt = 0) {
    let fd;
    try {
        fd = fs.openSync(
            LOCK_FILE,
            fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_WRONLY
        );
        fs.writeSync(fd, String(process.pid));
        fs.closeSync(fd);
    } catch (err) {
        if (err.code !== 'EEXIST') throw err;

        try {
            const raw = fs.readFileSync(LOCK_FILE, 'utf8').trim();
            const pid = parseInt(raw, 10);

            if (!isNaN(pid) && pid !== process.pid) {
                try {
                    process.kill(pid, 0);
                    return false; 
                } catch (killErr) {
                    if (killErr.code === 'EPERM') return false; 
                    process.stderr.write(`[lock] Verrou périmé (PID ${pid} mort) — nettoyage.\n`);
                    fs.unlinkSync(LOCK_FILE);
                    if (attempt >= MAX_LOCK_RETRIES) {
                        process.stderr.write(`[lock] Impossible d'acquérir le verrou après ${MAX_LOCK_RETRIES} tentatives.\n`);
                        return false;
                    }
                    return acquireLock(attempt + 1);
                }
            }
        } catch (_) {
            if (attempt >= MAX_LOCK_RETRIES) {
                process.stderr.write(`[lock] Impossible de lire le verrou après ${MAX_LOCK_RETRIES} tentatives.\n`);
                return false;
            }
            return acquireLock(attempt + 1);
        }

        return false;
    }

    const release = () => releaseLock();
    process.on('exit',    release);
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