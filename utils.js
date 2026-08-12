'use strict';
const fs = require('fs');
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
async function existsSafe(filePath) {
    try {
        await fs.promises.access(filePath);
        return true;
    } catch {
        return false;
    }
}
async function readJsonSafe(filePath, fallback = {}) {
    try { return JSON.parse(await fs.promises.readFile(filePath, 'utf8')); }
    catch (_) { return fallback; }
}

async function safeUnlink(filePath) {
    try {
        await fs.promises.unlink(filePath);
    } catch (err) {
        if (err.code !== 'ENOENT') console.error(`[utils.js] Échec suppression unlink : ${err.message}`);
    }
}

async function safeRm(targetPath, options = { recursive: true, force: true }) {
    try {
        await fs.promises.rm(targetPath, options);
    } catch (err) {
        if (err.code !== 'ENOENT') console.error(`[utils.js] Échec suppression rm : ${err.message}`);
    }
}

async function withConcurrency(limit, tasks) {
    const executing = new Set();
    const errors = [];
    for (let i = 0; i < tasks.length; i++) {
        const p = tasks[i]()
            .catch(e => {
                errors.push(e);
            })
            .finally(() => executing.delete(p));
        executing.add(p);
        if (executing.size >= limit) {
            await Promise.race(executing);
        }
    }
    await Promise.all(executing);
    if (errors.length > 0) {
        if (errors.length > 1) {
            process.stderr.write(
                `[concurrency] ${errors.length - 1} erreur(s) secondaire(s) ignorée(s) :\n` +
                errors.slice(1).map(e => `  - ${e.message}`).join('\n') + '\n'
            );
        }
        throw errors[0];
    }
}

const _tempFiles = new Set();
const _shutdownHooks = [];
function registerTemp(p) { _tempFiles.add(p); }
function unregisterTemp(p) { _tempFiles.delete(p); }
function cleanupTemps() {
    for (const f of _tempFiles) {
        try { if (fs.existsSync(f)) fs.unlinkSync(f); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[utils.js] Erreur silencieuse interceptée:', _.message || _); }
    }
}

function onShutdown(fn) { _shutdownHooks.push(fn); }
function _runShutdownHooks() {
    for (const fn of _shutdownHooks) { try { fn(); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[utils.js] Erreur silencieuse interceptée:', _.message || _); } }
}
function writeJsonAtomic(filePath, data) {
    return writeJsonAtomicAsync(filePath, data);
}
async function writeJsonAtomicAsync(filePath, data) {
    const tmp = filePath + '.tmp';
    registerTemp(tmp);
    let filehandle;
    try {
        filehandle = await fs.promises.open(tmp, 'w');
        await filehandle.writeFile(JSON.stringify(data, null, 2));
        await filehandle.sync();
    } finally {
        if (filehandle) await filehandle.close();
    }
    await fs.promises.rename(tmp, filePath);
    unregisterTemp(tmp);
}
function getCanonicalName(name) {
    if (!name || typeof name !== 'string') return 'UnknownInstance';
    return name.replace(/[<>:"/\\|?*\r\n\0'"`;$]/g, "").trim().substring(0, 100);
}

async function getCloudSettings(settingsPath) {
    let sets = { syncMode: 'SMART', maxRetries: 3, baseDelay: 2000 };
    if (await existsSafe(settingsPath)) {
        try {
            const parsed = JSON.parse(await fs.promises.readFile(settingsPath, 'utf8'));
            sets = { ...sets, ...parsed };
        } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[utils.js] Erreur silencieuse interceptée:', _.message || _); }
    }
    const retryOpts = { maxRetries: sets.maxRetries || 3, baseDelay: sets.retryBaseDelay || sets.baseDelay || 1500 };
    return { sets, retryOpts };
}
let _handlersSetup = false;
function setupProcessHandlers() {
    if (_handlersSetup) return;
    _handlersSetup = true;
    const shutdown = (signal) => {
        console.error(`[system] Signal reçu : ${signal}. Nettoyage en cours...`);
        _runShutdownHooks();
        cleanupTemps();
        process.exit(0);
    };
    process.on('SIGINT', () => shutdown('SIGINT'));
    process.on('SIGTERM', () => shutdown('SIGTERM'));
    process.on('uncaughtException', (err) => {
        try {
            process.stdout.write(JSON.stringify({
                type: 'ERROR',
                errorCode: 'UNCAUGHT_EXCEPTION',
                message: err.message || String(err),
            }) + '\n');
        } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[utils.js] Erreur silencieuse interceptée:', _.message || _); }
        process.stderr.write(`[CRITIQUE] ${err.stack || err}\n`);
        _runShutdownHooks();
        cleanupTemps();
        process.exit(1);
    });
    process.on('unhandledRejection', (reason) => {
        const msg = reason?.message || String(reason);
        try {
            process.stdout.write(JSON.stringify({
                type: 'ERROR',
                errorCode: 'UNHANDLED_REJECTION',
                message: msg,
            }) + '\n');
        } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[utils.js] Erreur silencieuse interceptée:', _.message || _); }
        process.stderr.write(`[CRITIQUE] Promesse rejetée : ${msg}\n`);
        _runShutdownHooks();
        cleanupTemps();
        process.exit(1);
    });
}

function reportProgress(step, value, instance, delta = null) {
    process.stdout.write(JSON.stringify({ type: 'PROGRESS', step, value, instance, delta }) + '\n');
}
function reportError(instance, message, errorCode = null, hasRollback = false) {
    process.stdout.write(JSON.stringify({ type: 'ERROR', instance, message, errorCode, hasRollback }) + '\n');
}
function reportInfo(instance, message) {
    process.stdout.write(JSON.stringify({ type: 'INFO', instance, message }) + '\n');
}
function reportSuccess(instance, message, baseChanged = false, deltasApplied = 0) {
    process.stdout.write(JSON.stringify({ type: 'SUCCESS', instance, message, base: baseChanged, deltas: deltasApplied }) + '\n');
}

module.exports = {
    getCanonicalName,
    getCloudSettings,
    checkConnectivity,
    readJsonSafe,
    writeJsonAtomic,
    writeJsonAtomicAsync,
    registerTemp,
    unregisterTemp,
    cleanupTemps,
    onShutdown,
    setupProcessHandlers,
    existsSafe,
    reportProgress,
    reportError,
    reportInfo,
    reportSuccess,
    safeUnlink,
    safeRm,
    withConcurrency,
};