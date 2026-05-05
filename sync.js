'use strict';

const fs     = require('fs');
const AdmZip = require('adm-zip');
const path   = require('path');
const dns    = require('dns').promises;

const { getInstancesFolder } = require('./paths');
const { getProvider }        = require('./provider');
const { generateManifest }   = require('./scanner');

const _tempFiles = new Set();
function _registerTemp(p)   { _tempFiles.add(p); }
function _unregisterTemp(p) { _tempFiles.delete(p); }
function _cleanupTemps() {
    for (const f of _tempFiles) {
        try { if (fs.existsSync(f)) fs.unlinkSync(f); } catch (_) {}
    }
}
process.on('SIGTERM', () => { _cleanupTemps(); process.exit(0); });
process.on('SIGINT',  () => { _cleanupTemps(); process.exit(0); });

function writeJsonAtomic(filePath, data) {
    const tmp = filePath + '.tmp';
    fs.writeFileSync(tmp, JSON.stringify(data, null, 2), 'utf8');
    fs.renameSync(tmp, filePath);
}

function readJsonSafe(filePath, fallback = {}) {
    try {
        return JSON.parse(fs.readFileSync(filePath, 'utf8'));
    } catch (_) {
        return fallback;
    }
}

function sanitizeInstanceName(name) {
    return name.replace(/[/\\:.]/g, '_');
}

function extractZip(zipPath, targetPath, onProgress) {
    const zip      = new AdmZip(zipPath);
    const entries  = zip.getEntries();
    const total    = entries.length;
    const resolved = path.resolve(targetPath);
    let done = 0, lastPct = -1;

    for (const entry of entries) {
        const dest    = path.join(targetPath, entry.entryName);
        const resDest = path.resolve(dest);

        if (!resDest.startsWith(resolved + path.sep) && resDest !== resolved) {
            process.stderr.write(`[ALERTE SÉCURITÉ] Entrée zip ignorée (path traversal) : ${entry.entryName}\n`);
            done++; continue;
        }

        if (entry.isDirectory) {
            fs.mkdirSync(dest, { recursive: true });
        } else {
            const content = zip.readFile(entry);
            if (content === null) {
                process.stderr.write(`[WARN] Entrée zip illisible ignorée : ${entry.entryName}\n`);
                done++; continue;
            }
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            fs.writeFileSync(dest, content);
        }
        done++;
        if (onProgress && total > 0) {
            const pct = Math.floor((done / total) * 100);
            if (pct !== lastPct && (pct >= lastPct + 3 || pct === 100)) { onProgress(pct); lastPct = pct; }
        }
    }
}

function applyDelta(deltaZipPath, targetPath, onProgress) {
    const zip      = new AdmZip(deltaZipPath);
    const entries  = zip.getEntries();
    const total    = entries.length;
    const resolved = path.resolve(targetPath);
    let done = 0, lastPct = -1;

    const deltaEntry = entries.find(e => e.entryName === '__delta__.json');
    // CORRIGÉ : JSON.parse sans try/catch crashait toute la sync sur delta corrompu
    let deltaInfo = { deletedFiles: [] };
    if (deltaEntry) {
        try { deltaInfo = JSON.parse(zip.readAsText(deltaEntry)); }
        catch (_) { process.stderr.write(`[WARN] __delta__.json corrompu, suppressions ignorées\n`); }
    }

    for (const entry of entries) {
        if (entry.entryName === '__delta__.json') { done++; continue; }

        const dest    = path.join(targetPath, entry.entryName);
        const resDest = path.resolve(dest);
        if (!resDest.startsWith(resolved + path.sep) && resDest !== resolved) {
            process.stderr.write(`[ALERTE SÉCURITÉ] Entrée delta ignorée (path traversal) : ${entry.entryName}\n`);
            done++; continue;
        }

        if (entry.isDirectory) {
            fs.mkdirSync(dest, { recursive: true });
        } else {
            const content = zip.readFile(entry);
            if (content === null) {
                process.stderr.write(`[WARN] Entrée delta illisible ignorée : ${entry.entryName}\n`);
                done++; continue;
            }
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            fs.writeFileSync(dest, content);
        }
        done++;
        if (onProgress && total > 0) {
            const pct = Math.floor((done / total) * 100);
            if (pct !== lastPct && (pct >= lastPct + 3 || pct === 100)) { onProgress(pct); lastPct = pct; }
        }
    }

    for (const relPath of (deltaInfo.deletedFiles || [])) {
        const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
        if (!path.resolve(absPath).startsWith(path.resolve(targetPath))) {
            process.stderr.write(`[ALERTE SÉCURITÉ] Suppression ignorée (hors instance) : ${absPath}\n`);
            continue;
        }
        try { if (fs.existsSync(absPath)) fs.unlinkSync(absPath); } catch (_) {}
    }
}

async function checkConnectivity() {
    const hosts = ['1.1.1.1', 'google.com', 'microsoft.com'];
    for (const host of hosts) {
        try { await dns.lookup(host); return true; } catch (_) {}
    }
    return false;
}

async function syncAllInstances() {
    try {
        const online = await checkConnectivity();
        if (!online) { console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible.' })); return; }

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');
        const args         = process.argv.slice(2);
        const force        = args.includes('--force');
        const isList       = args.includes('--list');
        const isDelete     = args.includes('--delete');
        const COMMANDS     = new Set(['sync', 'upload', 'check', 'login']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        let settings = { syncMode: 'SMART' };
        if (fs.existsSync(settingsPath)) {
            try { settings = { ...settings, ...JSON.parse(fs.readFileSync(settingsPath, 'utf8')) }; } catch (_) {}
        }

        const provider = await getProvider(settings, cwd);
        if (!provider) { console.log(JSON.stringify({ type: 'ERROR', message: "Compte non lié. Lance --login d'abord." })); return; }

        const cloudFiles = await provider.listFiles('GensHorizon_');
        const cloudIndex = {};
        for (const f of cloudFiles) cloudIndex[f.name] = f;

        if (isList) {
            const list = [...new Set(
                Object.keys(cloudIndex)
                    .filter(n => n.startsWith('GensHorizon_Backup_'))
                    .map(n => n.replace('GensHorizon_Backup_', '').replace('.zip', ''))
            )];
            const richList = list.map(instName => {
                const baseName   = `GensHorizon_Backup_${instName}.zip`;
                const baseFile   = cloudIndex[baseName];
                const deltaFiles = Object.keys(cloudIndex).filter(n => n.startsWith(`GensHorizon_Delta_${instName}_`) && n.endsWith('.zip'));
                const totalSizeBytes = deltaFiles.reduce((sum, n) => sum + (parseInt(cloudIndex[n].size, 10) || 0), 0) + (parseInt(baseFile?.size, 10) || 0);
                return { name: instName, deltaCount: deltaFiles.length, sizeBytes: totalSizeBytes, lastBackup: baseFile?.modifiedTime || null };
            });
            console.log(JSON.stringify({ type: 'CLOUD_LIST', data: list, richData: richList }));
            return;
        }

        if (isDelete && targetInstance) {
            const safeTarget = sanitizeInstanceName(targetInstance);
            const toDelete = Object.keys(cloudIndex).filter(n =>
                n === `GensHorizon_Backup_${safeTarget}.zip` ||
                n === `GensHorizon_Manifest_${safeTarget}.json` ||
                n.startsWith(`GensHorizon_Delta_${safeTarget}_`)
            );
            for (const n of toDelete) await provider.deleteFile(cloudIndex[n].id);
            const manifestPath = path.join(cwd, `manifest_${safeTarget}.json`);
            if (fs.existsSync(manifestPath)) fs.unlinkSync(manifestPath);
            if (fs.existsSync(syncInfoPath)) {
                const syncState = readJsonSafe(syncInfoPath);
                delete syncState[targetInstance];
                writeJsonAtomic(syncInfoPath, syncState);
            }
            console.log(JSON.stringify({ type: 'SUCCESS', instance: targetInstance, message: 'Supprimé du cloud.' }));
            return;
        }

        let syncState = fs.existsSync(syncInfoPath) ? readJsonSafe(syncInfoPath) : {};

        const instancesToSync = targetInstance
            ? [targetInstance]
            : [...new Set(Object.keys(cloudIndex).filter(n => n.startsWith('GensHorizon_Backup_')).map(n => n.replace('GensHorizon_Backup_', '').replace('.zip', '')))];

        for (const inst of instancesToSync) {
            try {
                const safeInst    = sanitizeInstanceName(inst);
                const baseName    = `GensHorizon_Backup_${safeInst}.zip`;
                const baseFile    = cloudIndex[baseName];

                if (targetInstance) console.log(JSON.stringify({ type: 'PROGRESS', step: 'CHECKING', value: 0, instance: inst }));

                if (!baseFile) { console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${inst} n'existe pas encore sur le Cloud.` })); continue; }

                const targetPath = path.join(getInstancesFolder(), inst);
                const lastSync   = syncState[inst] ? new Date(syncState[inst]).getTime() : 0;
                const baseTime   = new Date(baseFile.modifiedTime).getTime();

                const deltaFiles = Object.keys(cloudIndex)
                    .filter(n => n.startsWith(`GensHorizon_Delta_${safeInst}_`) && n.endsWith('.zip'))
                    .map(n => { const ts = parseInt(n.replace(`GensHorizon_Delta_${safeInst}_`, '').replace('.zip', ''), 10); return { name: n, file: cloudIndex[n], ts }; })
                    .filter(d => !isNaN(d.ts))
                    .sort((a, b) => a.ts - b.ts);

                const pendingDeltas = deltaFiles.filter(d => d.ts > lastSync || force);
                const baseChanged  = baseTime > lastSync || force;

                if (!baseChanged && pendingDeltas.length === 0) { console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${inst} est déjà à jour.` })); continue; }
                if (!fs.existsSync(targetPath)) fs.mkdirSync(targetPath, { recursive: true });

                if (baseChanged) {
                    const tempBase = path.join(cwd, `download_base_${safeInst}.zip`);
                    _registerTemp(tempBase);
                    try {
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'DOWNLOADING', value: 0, instance: inst }));
                        await provider.downloadFile(baseFile.id, tempBase,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'DOWNLOADING', value: pct, instance: inst })),
                            parseInt(baseFile.size, 10) || 0);
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: 0, instance: inst }));
                        extractZip(tempBase, targetPath, (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: pct, instance: inst })));
                    } finally {
                        try { if (fs.existsSync(tempBase)) fs.unlinkSync(tempBase); } catch (_) {}
                        _unregisterTemp(tempBase);
                    }
                }

                for (const delta of pendingDeltas) {
                    const tempDelta = path.join(cwd, `download_delta_${safeInst}_${delta.ts}.zip`);
                    _registerTemp(tempDelta);
                    try {
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'APPLYING_DELTA', value: 0, instance: inst, delta: delta.name }));
                        await provider.downloadFile(delta.file.id, tempDelta, null, parseInt(delta.file.size, 10) || 0);
                        applyDelta(tempDelta, targetPath, (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'APPLYING_DELTA', value: pct, instance: inst, delta: delta.name })));
                    } finally {
                        try { if (fs.existsSync(tempDelta)) fs.unlinkSync(tempDelta); } catch (_) {}
                        _unregisterTemp(tempDelta);
                    }
                }

                const lastDelta = pendingDeltas.length > 0 ? pendingDeltas[pendingDeltas.length - 1] : null;
                syncState[inst] = lastDelta ? new Date(lastDelta.ts).toISOString() : baseFile.modifiedTime;
                // CORRIGÉ : écriture atomique pour éviter corruption sur SIGKILL
                writeJsonAtomic(syncInfoPath, syncState);

                try {
                    const newManifest = await generateManifest(targetPath);
                    writeJsonAtomic(path.join(cwd, `manifest_${safeInst}.json`), newManifest);
                } catch (_) {}

                const deltasApplied = pendingDeltas.length;
                console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, base: baseChanged, deltas: deltasApplied,
                    message: baseChanged ? `Base + ${deltasApplied} delta(s) appliqué(s).` : `${deltasApplied} delta(s) appliqué(s).` }));

            } catch (instErr) {
                console.log(JSON.stringify({ type: 'ERROR', instance: inst, message: instErr.message }));
            }
        }

    } catch (e) {
        if (e.message && (e.message.includes('invalid_grant') || e.message.includes('invalid_token'))) {
            console.log(JSON.stringify({ type: 'ERROR', message: 'Session expirée. Veuillez ré-associer votre compte.' }));
        } else if (e.code === 'ENOTFOUND' || e.code === 'EAI_AGAIN' || e.code === 'ECONNREFUSED') {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible.' }));
        } else {
            console.log(JSON.stringify({ type: 'ERROR', message: e.message }));
        }
    }
}

syncAllInstances();
