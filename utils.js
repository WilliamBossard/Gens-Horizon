'use strict';

const fs  = require('fs');
const dns = require('dns').promises;

async function checkConnectivity() {
    const hosts = ['1.1.1.1', 'google.com', 'microsoft.com'];
    try {
        await Promise.any(hosts.map(h => dns.lookup(h)));
        return true;
    } catch {
        return false;
    }
}


function readJsonSafe(filePath, fallback = {}) {
    try { return JSON.parse(fs.readFileSync(filePath, 'utf8')); }
    catch (_) { return fallback; }
}


const _tempFiles = new Set();

function registerTemp(p)   { _tempFiles.add(p); }
function unregisterTemp(p) { _tempFiles.delete(p); }
function cleanupTemps() {
    for (const f of _tempFiles) {
        try { if (fs.existsSync(f)) fs.unlinkSync(f); } catch (_) {}
    }
}


function writeJsonAtomic(filePath, data) {
    const tmp = filePath + '.tmp';
    registerTemp(tmp);
    const fd = fs.openSync(tmp, 'w');
    fs.writeSync(fd, JSON.stringify(data, null, 2));
    fs.fsyncSync(fd); 
    fs.closeSync(fd);
    fs.renameSync(tmp, filePath);
    unregisterTemp(tmp);
}

function getCanonicalName(name) {
    if (!name) return "";
    return String(name).replace(/[^a-z0-9]/gi, "_");
}

let _handlersSetup = false;
function setupProcessHandlers() {
    if (_handlersSetup) return;
    _handlersSetup = true;

    const shutdown = (signal) => {
        console.error(`[system] Signal reçu : ${signal}. Nettoyage en cours...`);
        cleanupTemps();
        process.exit(0);
    };

    process.on('SIGINT', () => shutdown('SIGINT'));
    process.on('SIGTERM', () => shutdown('SIGTERM'));
    process.on('uncaughtException', (err) => {
        console.error('[CRITIQUE] Erreur inattendue :', err);
        cleanupTemps();
        process.exit(1);
    });
}

module.exports = {
    getCanonicalName,
    checkConnectivity,
    readJsonSafe,
    writeJsonAtomic,
    registerTemp,
    unregisterTemp,
    cleanupTemps,
    setupProcessHandlers,
};