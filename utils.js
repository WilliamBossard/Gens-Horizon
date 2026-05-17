'use strict';

const fs  = require('fs');
const dns = require('dns').promises;

async function checkConnectivity() {
    const hosts = ['1.1.1.1', 'google.com', 'microsoft.com'];
    for (const host of hosts) {
        try { await dns.lookup(host); return true; } catch (_) {}
    }
    return false;
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
    try {
        fs.writeFileSync(tmp, JSON.stringify(data, null, 2), 'utf8');
        fs.renameSync(tmp, filePath);
    } finally {
        unregisterTemp(tmp);
    }
}


function sanitizeInstanceName(name) {
    return name.replace(/[/\\:.]/g, '_');
}

function setupProcessHandlers() {
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
};