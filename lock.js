'use strict';
const fs = require('fs');
const path = require('path');
const { getHorizonDataDir } = require('./paths');
const { onShutdown } = require('./utils');
const LOCK_FILE = path.join(getHorizonDataDir(), 'horizon.lock');
const MAX_LOCK_RETRIES = 5;
const STALE_LOCK_MS = 2 * 60 * 60 * 1000;

let heartbeatInterval = null;

async function isLockStale() {
    try {
        const stat = await fs.promises.stat(LOCK_FILE);
        const age = Date.now() - stat.mtimeMs;
        if (age > 30000) return true;
        
        const content = (await fs.promises.readFile(LOCK_FILE, 'utf8')).trim();
        const pid = parseInt(content, 10);
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
    } catch (err) {
        if (err.code !== 'ENOENT') process.stderr.write(`[lock] Erreur lecture verrou: ${err.message}\n`);
        return true;
    }
}
async function acquireLock(attempt = 0) {
    if (attempt >= MAX_LOCK_RETRIES) {
        process.stderr.write("[lock] Impossible d'acquérir le verrou après " + MAX_LOCK_RETRIES + " tentatives.\n");
        return false;
    }

    if (await existsSafe(LOCK_FILE)) {
        if (await isLockStale()) {
            process.stderr.write('[lock] Verrou périmé détecté — nettoyage.\n');
            try {
                await fs.promises.unlink(LOCK_FILE);
            } catch (err) {
                if (err.code !== 'ENOENT') process.stderr.write(`[lock] Erreur suppression verrou périmé: ${err.message}\n`);
                // Si le système refuse la suppression (permissions OS), on abandonne proprement
                // plutôt que de bloquer l'event loop avec un busy-wait synchrone.
                process.stderr.write('[lock] Impossible de supprimer le verrou périmé (erreur OS).\n');
                return false;
            }
        } else {
            return false;
        }
    }

    try {
        const fh = await fs.promises.open(
            LOCK_FILE,
            fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_WRONLY
        );
        await fh.writeFile(String(process.pid));
        await fh.close();
    } catch (err) {
        if (err.code !== 'EEXIST') throw err;
        return await acquireLock(attempt + 1);
    }

    heartbeatInterval = setInterval(() => {
        try {
            const now = new Date();
            fs.promises.utimes(LOCK_FILE, now, now).catch((err) => {
                if (err && ['EPERM', 'EACCES'].includes(err.code)) {
                    process.stderr.write(`[lock] Erreur de permission sur le heartbeat du verrou.\n`);
                }
            });
        } catch (err) { 
            process.stderr.write(`[lock] Erreur inattendue dans le heartbeat: ${err.message}\n`);
        }
    }, 5000);
    
    if (heartbeatInterval && heartbeatInterval.unref) {
        heartbeatInterval.unref();
    }

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
    } catch (err) { 
        if (err && ['EPERM', 'EACCES'].includes(err.code)) {
            process.stderr.write(`[lock] Erreur OS critique (Permissions) lors de la libération du verrou: ${err.message}\n`);
        }
    }
}

module.exports = { acquireLock, releaseLock, LOCK_FILE };

async function existsSafe(p) {
    try {
        // Enforce preload sandbox check if it's in renderer context and enforceReadSandbox exists
        if (typeof enforceReadSandbox !== 'undefined') p = enforceReadSandbox(p, true);
        await fs.promises.access(p);
        return true;
    } catch {
        return false;
    }
}
