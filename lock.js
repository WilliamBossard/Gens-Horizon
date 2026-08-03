'use strict';
const fs = require('fs');
const path = require('path');
const { getHorizonDataDir } = require('./paths');
const { onShutdown } = require('./utils');
const LOCK_FILE = path.join(getHorizonDataDir(), 'horizon.lock');
const MAX_LOCK_RETRIES = 5;
const STALE_LOCK_MS = 2 * 60 * 60 * 1000;

let heartbeatInterval = null;

function isLockStale() {
    try {
        const content = fs.readFileSync(LOCK_FILE, 'utf8').trim();
        const pid = parseInt(content, 10);
        const age = Date.now() - fs.statSync(LOCK_FILE).mtimeMs;
        if (!isNaN(pid)) {
            try { 
                process.kill(pid, 0); 
                return false; 
            } catch (e) { 
                if (e.code === 'ESRCH') return true; 
                if (e.code === 'EPERM') return false;
            }
        }
        return age > STALE_LOCK_MS;
    } catch (_) {
        return true;
    }
}

function acquireLock(attempt = 0) {
    if (attempt >= MAX_LOCK_RETRIES) {
        process.stderr.write("[lock] Impossible d'acquérir le verrou après " + MAX_LOCK_RETRIES + " tentatives.\n");
        return false;
    }

    if (fs.existsSync(LOCK_FILE)) {
        if (isLockStale()) {
            process.stderr.write('[lock] Verrou périmé détecté — nettoyage.\n');
            try {
                fs.unlinkSync(LOCK_FILE);
            } catch (_) {
                // Si le système refuse la suppression (permissions OS), on abandonne proprement
                // plutôt que de bloquer l'event loop avec un busy-wait synchrone.
                process.stderr.write('[lock] Impossible de supprimer le verrou périmé (erreur OS).\n');
                return false;
            }
        } else {
            return false;
        }
    }

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
        return acquireLock(attempt + 1);
    }

    heartbeatInterval = setInterval(() => {
        try {
            const now = new Date();
            fs.utimesSync(LOCK_FILE, now, now);
        } catch (_) { }
    }, 5000);

    onShutdown(() => releaseLock());
    process.once('exit', () => releaseLock());
    return true;
}

function releaseLock() {
    try {
        if (heartbeatInterval) {
            clearInterval(heartbeatInterval);
            heartbeatInterval = null;
        }
        if (!fs.existsSync(LOCK_FILE)) return;
        const pid = parseInt(fs.readFileSync(LOCK_FILE, 'utf8').trim(), 10);
        if (pid === process.pid) fs.unlinkSync(LOCK_FILE);
    } catch (_) { }
}

module.exports = { acquireLock, releaseLock, LOCK_FILE };