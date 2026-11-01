'use strict';
const fs = require('fs');
const crypto = require('crypto');
const dns = require('dns').promises;

async function checkConnectivity() {
    const hosts = ['google.com', 'cloudflare.com', 'microsoft.com'];
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
    const rand = crypto.randomBytes(4).toString('hex');
    const tmp = `${filePath}.${process.pid}.${Date.now()}.${rand}.tmp`;
    registerTemp(tmp);
    let filehandle;
    try {
        filehandle = await fs.promises.open(tmp, 'w');
        await filehandle.writeFile(JSON.stringify(data, null, 2));
        await filehandle.sync();
    } finally {
        if (filehandle) await filehandle.close();
    }
    try {
        let renamed = false;
        for (let i = 0; i < 5; i++) {
            try {
                await fs.promises.rename(tmp, filePath);
                renamed = true;
                break;
            } catch (rErr) {
                if (i === 4) throw rErr;
                await new Promise(r => setTimeout(r, 25 * (i + 1)));
            }
        }
    } catch (err) {
        await safeUnlink(tmp);
        throw err;
    } finally {
        unregisterTemp(tmp);
    }
}

function getCanonicalName(name) {
    if (!name || typeof name !== 'string') return 'UnknownInstance';
    const clean = name.replace(/[<>:"/\\|?*\r\n\0'"`;$]/g, "").trim().substring(0, 100);
    return clean || 'UnknownInstance';
}

function parseTargetInstance(args) {
    if (!args || !Array.isArray(args)) return null;
    const instIdx = args.indexOf('--instance');
    if (instIdx !== -1 && args[instIdx + 1] && !args[instIdx + 1].startsWith('--')) {
        return args[instIdx + 1];
    }
    const eqArg = args.find(a => a.startsWith('--instance='));
    if (eqArg) {
        return eqArg.split('=')[1];
    }
    const COMMANDS = new Set(['sync', 'upload', 'check', 'login', 'quota', 'rollback', 'delete']);
    const FLAGS_WITH_ARGS = new Set(['--provider', '--lang']);
    const KNOWN_PROVIDERS = new Set(['google', 'dropbox', 'onedrive']);
    for (let i = 0; i < args.length; i++) {
        const arg = args[i];
        if (arg.startsWith('--')) {
            if (FLAGS_WITH_ARGS.has(arg)) {
                i++; // Sauter la valeur liée à ce flag
            }
            continue;
        }
        if (COMMANDS.has(arg) || KNOWN_PROVIDERS.has(arg.toLowerCase())) continue;
        return arg;
    }
    return null;
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
    parseTargetInstance,
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
