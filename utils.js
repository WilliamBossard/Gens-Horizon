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
    let success = false;
    try {
        fs.writeFileSync(tmp, JSON.stringify(data, null, 2), 'utf8');
        fs.renameSync(tmp, filePath);
        success = true;
    } finally {
        if (!success) {
            try { fs.unlinkSync(tmp); } catch (_) {}
        }
        unregisterTemp(tmp);
    }
}


function sanitizeInstanceName(name) {
    return name.replace(/[/\\:.]/g, '_');
}

function getFolderFromName(name) {
    if (!name) return '';
    return name.replace(/[^a-z0-9]/gi, '_');
}

let _handlersSetup = false;
function setupProcessHandlers() {
    if (_handlersSetup) return;
    _handlersSetup = true;
    process.on('exit', cleanupTemps);
    process.on('uncaughtException', (err) => {
        console.error('Erreur critique inattendue :', err);
        cleanupTemps();
        process.exit(1);
    });
}

module.exports = {
    checkConnectivity,
    readJsonSafe,
    writeJsonAtomic,
    sanitizeInstanceName,
    registerTemp,
    unregisterTemp,
    cleanupTemps,
    setupProcessHandlers,
    getFolderFromName,
};