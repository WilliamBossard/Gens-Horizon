'use strict';

const fs     = require('fs');
const yauzl  = require('yauzl');
const path   = require('path');

const { getInstancesFolder }           = require('./paths');
const { getProvider }                  = require('./provider');
const { generateManifest }             = require('./scanner');
const { acquireLock, releaseLock }     = require('./lock');
const { withRetry }                    = require('./retry');
const {
    checkConnectivity,
    readJsonSafe,
    writeJsonAtomic,
    sanitizeInstanceName,
    registerTemp,
    unregisterTemp,
    setupProcessHandlers,
    getFolderFromName,
} = require('./utils');

setupProcessHandlers();

function verifyZipIntegrity(zipPath) {
    return new Promise((resolve, reject) => {
        yauzl.open(zipPath, { lazyEntries: true }, (err, zipfile) => {
            if (err) return reject(new Error(`Archive invalide : ${err.message}`));
            
            zipfile.readEntry();
            zipfile.on("entry", () => {
                zipfile.close();
                resolve();
            });
            zipfile.on("end", () => { zipfile.close(); reject(new Error("Archive ZIP vide.")); });
            zipfile.on("error", (e) => { zipfile.close(); reject(new Error(`Archive corrompue : ${e.message}`)); });
        });
    });
}

function extractZip(zipPath, targetPath, onProgress) {
    return new Promise((resolve, reject) => {
        const resolvedTarget = path.resolve(targetPath);
        yauzl.open(zipPath, { lazyEntries: true }, (err, zipfile) => {
            if (err) return reject(err);
            
            const total = zipfile.entryCount;
            let done = 0, lastPct = -1;

            zipfile.readEntry();
            zipfile.on("entry", (entry) => {
                const dest = path.join(targetPath, entry.fileName);
                const resDest = path.resolve(dest);
                
                if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                    process.stderr.write(`[ALERTE SÉCURITÉ] Entrée zip ignorée : ${entry.fileName}\n`);
                    done++; zipfile.readEntry(); return;
                }

                if (/\/$/.test(entry.fileName)) {
                    fs.mkdirSync(dest, { recursive: true });
                    done++; reportProgress(); zipfile.readEntry();
                } else {
                    fs.mkdirSync(path.dirname(dest), { recursive: true });
                    zipfile.openReadStream(entry, (err, readStream) => {
                        if (err) { zipfile.close(); return reject(err); }
                        const writeStream = fs.createWriteStream(dest);
                        readStream.pipe(writeStream);
                        writeStream.on("close", () => { done++; reportProgress(); zipfile.readEntry(); });
                        writeStream.on("error", (e) => { readStream.destroy(); writeStream.destroy(); zipfile.close(); reject(e); });
                        readStream.on("error", (e) => { writeStream.destroy(); zipfile.close(); reject(e); });
                    });
                }
            });

            function reportProgress() {
                if (onProgress && total > 0) {
                    const pct = Math.floor((done / total) * 100);
                    if (pct !== lastPct && (pct >= lastPct + 3 || pct === 100)) { onProgress(pct); lastPct = pct; }
                }
            }

            zipfile.on("end", () => resolve());
            zipfile.on("error", (e) => { zipfile.close(); reject(e); });
        });
    });
}

function applyDelta(deltaZipPath, targetPath, onProgress) {
    return new Promise((resolve, reject) => {
        const resolvedTarget = path.resolve(targetPath);
        let deltaInfo = { deletedFiles: [] };

        yauzl.open(deltaZipPath, { lazyEntries: true }, (err, zipfile) => {
            if (err) return reject(err);
            
            const total = zipfile.entryCount;
            let done = 0, lastPct = -1;

            zipfile.readEntry();
            zipfile.on("entry", (entry) => {
                if (entry.fileName === '__delta__.json') {
                    zipfile.openReadStream(entry, (err, readStream) => {
                        if (err) { zipfile.close(); return reject(err); }
                        let data = '';
                        readStream.on('data', chunk => data += chunk);
                        readStream.on('end', () => {
                            try { deltaInfo = JSON.parse(data); } catch (_) {}
                            done++; reportProgress(); zipfile.readEntry();
                        });
                        readStream.on('error', (e) => { zipfile.close(); reject(e); }); 
                    });
                    return;
                }

                const dest = path.join(targetPath, entry.fileName);
                const resDest = path.resolve(dest);
                if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                    done++; zipfile.readEntry(); return;
                }

                if (/\/$/.test(entry.fileName)) {
                    fs.mkdirSync(dest, { recursive: true });
                    done++; reportProgress(); zipfile.readEntry();
                } else {
                    fs.mkdirSync(path.dirname(dest), { recursive: true });
                    zipfile.openReadStream(entry, (err, readStream) => {
                        if (err) { zipfile.close(); return reject(err); }
                        const writeStream = fs.createWriteStream(dest);
                        readStream.pipe(writeStream);
                        writeStream.on("close", () => { done++; reportProgress(); zipfile.readEntry(); });
                        writeStream.on("error", (e) => { readStream.destroy(); writeStream.destroy(); zipfile.close(); reject(e); });
                        readStream.on("error", (e) => { writeStream.destroy(); zipfile.close(); reject(e); });
                    });
                }
            });

            function reportProgress() {
                if (onProgress && total > 0) {
                    const pct = Math.floor((done / total) * 100);
                    if (pct !== lastPct && (pct >= lastPct + 3 || pct === 100)) { onProgress(pct); lastPct = pct; }
                }
            }

            zipfile.on("end", () => {
                for (const relPath of (deltaInfo.deletedFiles || [])) {
                    const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
                    if (!path.resolve(absPath).startsWith(resolvedTarget + path.sep)) continue;
                    try { if (fs.existsSync(absPath)) fs.rmSync(absPath, { recursive: true, force: true }); } catch (_) {}
                }
                resolve();
            });
            zipfile.on("error", (e) => { zipfile.close(); reject(e); });
        });
    });
}

function createRollbackSnapshot(instancePath, safeInst) {
    const instDir    = path.dirname(instancePath);
    const timestamp  = Date.now();
    const rollbackTo = path.join(instDir, `${safeInst}_rollback_${timestamp}`);

    try {
        for (const entry of fs.readdirSync(instDir)) {
            if (entry.startsWith(`${safeInst}_rollback_`)) {
                fs.rmSync(path.join(instDir, entry), { recursive: true, force: true });
            }
        }
    } catch (_) {}

    try {
        fs.cpSync(instancePath, rollbackTo, { recursive: true });
        process.stderr.write(`[sync] Rollback créé : ${path.basename(rollbackTo)}\n`);
        return rollbackTo;
    } catch (e) {
        process.stderr.write(`[sync] Impossible de créer le rollback : ${e.message}\n`);
        return null;
    }
}

function cleanupRollback(rollbackPath) {
    if (!rollbackPath) return;
    try { fs.rmSync(rollbackPath, { recursive: true, force: true }); }
    catch (_) {}
}

async function syncAllInstances() {
    const args   = process.argv.slice(2);
    const isList = args.includes('--list');

    if (!isList) {
        if (!acquireLock()) {
            console.log(JSON.stringify({
                type     : 'ERROR',
                errorCode: 'ERR_ALREADY_RUNNING',
                message  : 'ERR_ALREADY_RUNNING',
            }));
            process.exit(1);
        }
    }

    try {
        const online = await checkConnectivity();
        if (!online) {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible.' }));
            return;
        }

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');
        const force        = args.includes('--force');
        const isDelete     = args.includes('--delete');
        const COMMANDS     = new Set(['sync', 'upload', 'check', 'login', 'quota', 'rollback']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        let settings = { syncMode: 'SMART', maxRetries: 3, retryBaseDelay: 1500 };
        if (fs.existsSync(settingsPath)) {
            try { settings = { ...settings, ...JSON.parse(fs.readFileSync(settingsPath, 'utf8')) }; } catch (_) {}
        }

        const retryOpts = { maxRetries: settings.maxRetries || 3, baseDelay: settings.retryBaseDelay || 1500 };

        const provider = await getProvider(settings, cwd);
        if (!provider) {
            console.log(JSON.stringify({ 
    type: 'ERROR', 
    errorCode: 'AUTH_EXPIRED', 
    message: "Session expirée. Veuillez lier à nouveau votre compte depuis les paramètres." 
}));
            return;
        }

        const cloudFiles = await withRetry(() => provider.listFiles('GensHorizon_'), { ...retryOpts, label: 'listFiles' });
        const cloudIndex = {};
        for (const f of cloudFiles) cloudIndex[f.name] = f;

        if (isList) {
            const metaFiles = Object.keys(cloudIndex).filter(n => n.startsWith('GensHorizon_Meta_'));
            const downloadPromises = metaFiles.map(async (mName) => {
                const instName = mName.replace('GensHorizon_Meta_', '').replace('.json', '');
                const localMetaPath = path.join(cwd, `meta_${instName}.json`);
                
                let needsDownload = true;
                if (fs.existsSync(localMetaPath)) {
                    const localStat = fs.statSync(localMetaPath);
                    const cloudTime = new Date(cloudIndex[mName].modifiedTime).getTime();
                    if (localStat.mtime.getTime() >= cloudTime) needsDownload = false;
                }
                
                if (needsDownload) {
                    try {
                        const data = await provider.downloadJSON(cloudIndex[mName].id);
                        writeJsonAtomic(localMetaPath, data);
                    } catch(_) {} 
                }
            });
            await Promise.all(downloadPromises);
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
                
                let realName = instName;
                const localMetaPath = path.join(cwd, `meta_${instName}.json`);
                if (fs.existsSync(localMetaPath)) {
                    try {
                        const metaObj = JSON.parse(fs.readFileSync(localMetaPath, 'utf8'));
                        if (metaObj.realName) realName = metaObj.realName;
                    } catch(_) {}
                }

                return { 
                    name: instName, 
                    realName: realName, 
                    deltaCount: deltaFiles.length, 
                    sizeBytes: totalSizeBytes, 
                    lastBackup: baseFile?.modifiedTime || null 
                };
            });
            console.log(JSON.stringify({ type: 'CLOUD_LIST', data: list, richData: richList }));
            return;
        }

        if (isDelete && targetInstance) {
            const safeTarget = sanitizeInstanceName(targetInstance);
            const toDelete   = Object.keys(cloudIndex).filter(n =>
                n === `GensHorizon_Backup_${safeTarget}.zip` ||
                n === `GensHorizon_Manifest_${safeTarget}.json` ||
                n === `GensHorizon_Meta_${safeTarget}.json` ||
                n.startsWith(`GensHorizon_Delta_${safeTarget}_`)
            );
            for (const n of toDelete) {
                await withRetry(() => provider.deleteFile(cloudIndex[n].id), { ...retryOpts, label: `deleteFile(${n})` });
            }
            const manifestPath = path.join(cwd, `manifest_${safeTarget}.json`);
            if (fs.existsSync(manifestPath)) fs.unlinkSync(manifestPath);
            if (fs.existsSync(syncInfoPath)) {
                const syncState = readJsonSafe(syncInfoPath);
                delete syncState[safeTarget];
                writeJsonAtomic(syncInfoPath, syncState);
            }
            const metaPath = path.join(cwd, `meta_${safeTarget}.json`);
            if (fs.existsSync(metaPath)) fs.unlinkSync(metaPath);
            console.log(JSON.stringify({ type: 'SUCCESS', instance: targetInstance, message: 'Supprimé du cloud.' }));
            return;
        }

        let syncState = readJsonSafe(syncInfoPath);

        const instancesToSync = targetInstance
            ? [targetInstance]
            : [...new Set(Object.keys(cloudIndex)
                .filter(n => n.startsWith('GensHorizon_Backup_'))
                .map(n => n.replace('GensHorizon_Backup_', '').replace('.zip', '')))];

        for (const inst of instancesToSync) {
            let rollbackPath = null;
            try {
                const safeInst = sanitizeInstanceName(inst);
                const baseName = `GensHorizon_Backup_${safeInst}.zip`;
                const baseFile = cloudIndex[baseName];

                if (targetInstance) {
                    console.log(JSON.stringify({ type: 'PROGRESS', step: 'CHECKING', value: 0, instance: inst }));
                }

                if (!baseFile) {
                    if (targetInstance) {
                        console.log(JSON.stringify({ type: 'ERROR', errorCode: 'NOT_ON_CLOUD', instance: inst, message: `${inst} n'existe pas sur le Cloud.` }));
                    } else {
                        console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${inst} n'existe pas encore sur le Cloud.` }));
                    }
                    continue;
                }

                const targetPath = path.join(getInstancesFolder(), getFolderFromName(inst));
                const lastSync   = syncState[safeInst] ? new Date(syncState[safeInst]).getTime() : 0;
                const baseTime   = new Date(baseFile.modifiedTime).getTime();

                const deltaFiles = Object.keys(cloudIndex)
                    .filter(n => n.startsWith(`GensHorizon_Delta_${safeInst}_`) && n.endsWith('.zip'))
                    .map(n => {
                        const ts = parseInt(n.replace(`GensHorizon_Delta_${safeInst}_`, '').replace('.zip', ''), 10);
                        return { name: n, file: cloudIndex[n], ts };
                    })
                    .filter(d => !isNaN(d.ts))
                    .sort((a, b) => a.ts - b.ts);

                const pendingDeltas = deltaFiles.filter(d => d.ts > lastSync || force);
                const baseChanged   = baseTime > lastSync || force;

                if (!baseChanged && pendingDeltas.length === 0) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${inst} est déjà à jour.` }));
                    continue;
                }

                if (!fs.existsSync(targetPath)) {
                    fs.mkdirSync(targetPath, { recursive: true });
                } else if (baseChanged || pendingDeltas.length > 0) { 
                    rollbackPath = createRollbackSnapshot(targetPath, safeInst);
                }

                if (baseChanged) {
                    const tempBase = path.join(cwd, `download_base_${safeInst}.zip`);
                    registerTemp(tempBase);
                    try {
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'DOWNLOADING', value: 0, instance: inst }));

                        await withRetry(
                            () => provider.downloadFile(
                                baseFile.id, tempBase,
                                (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'DOWNLOADING', value: pct, instance: inst })),
                                parseInt(baseFile.size, 10) || 0
                            ),
                            { ...retryOpts, label: `downloadBase(${inst})` }
                        );

                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'VERIFYING', value: 0, instance: inst }));
                        await verifyZipIntegrity(tempBase);
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'VERIFYING', value: 100, instance: inst }));

                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: 0, instance: inst }));
                        await extractZip(tempBase, targetPath,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: pct, instance: inst }))
                        );
                    } finally {
                        try { if (fs.existsSync(tempBase)) fs.unlinkSync(tempBase); } catch (_) {}
                        unregisterTemp(tempBase);
                    }
                }

                for (const delta of pendingDeltas) {
                    const tempDelta = path.join(cwd, `download_delta_${safeInst}_${delta.ts}.zip`);
                    registerTemp(tempDelta);
                    try {
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'APPLYING_DELTA', value: 0, instance: inst, delta: delta.name }));

                        await withRetry(
                            () => provider.downloadFile(delta.file.id, tempDelta, null, parseInt(delta.file.size, 10) || 0),
                            { ...retryOpts, label: `downloadDelta(${inst}/${delta.name})` }
                        );

                        await verifyZipIntegrity(tempDelta);

                        await applyDelta(tempDelta, targetPath,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'APPLYING_DELTA', value: pct, instance: inst, delta: delta.name }))
                        );
                    } finally {
                        try { if (fs.existsSync(tempDelta)) fs.unlinkSync(tempDelta); } catch (_) {}
                        unregisterTemp(tempDelta);
                    }
                }

                const lastDelta = pendingDeltas.length > 0 ? pendingDeltas[pendingDeltas.length - 1] : null;
                syncState[safeInst] = lastDelta ? new Date(lastDelta.ts).toISOString() : baseFile.modifiedTime;
                writeJsonAtomic(syncInfoPath, syncState);

                cleanupRollback(rollbackPath);
                rollbackPath = null;

                try {
                    const newManifest = await generateManifest(targetPath);
                    writeJsonAtomic(path.join(cwd, `manifest_${safeInst}.json`), newManifest);
                } catch (_) {}

                const deltasApplied = pendingDeltas.length;
                console.log(JSON.stringify({
                    type    : 'SUCCESS',
                    instance: inst,
                    base    : baseChanged,
                    deltas  : deltasApplied,
                    message : baseChanged
                        ? `Base + ${deltasApplied} delta(s) appliqué(s).`
                        : `${deltasApplied} delta(s) appliqué(s).`,
                }));

            } catch (instErr) {
                const rollbackMsg = rollbackPath
                    ? ` Un rollback est disponible (lance --rollback ${inst} pour restaurer).`
                    : '';
                console.log(JSON.stringify({
                    type       : 'ERROR',
                    instance   : inst,
                    message    : instErr.message + rollbackMsg,
                    hasRollback: !!rollbackPath,
                }));
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
    } finally {
        if (!process.argv.includes('--list')) releaseLock();
    }
}

syncAllInstances();