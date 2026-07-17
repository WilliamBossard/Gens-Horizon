'use strict';
const fs = require('fs');
const unzipper = require('unzipper');
const path = require('path');
const { getInstancesFolder, getHorizonDataDir } = require('./paths');
const { getProvider } = require('./provider');
const { generateManifest, withConcurrency } = require('./scanner');
const { acquireLock, releaseLock } = require('./lock');
const { withRetry } = require('./retry');
const {
    checkConnectivity,
    readJsonSafe,
    writeJsonAtomic,
    writeJsonAtomicAsync,
    getCanonicalName,
    registerTemp,
    unregisterTemp,
    setupProcessHandlers,
} = require('./utils');
const { verifyZipIntegrity, extractZip, applyDelta } = require('./zip-utils');
const { getCloudIndexAndCleanDuplicates } = require('./cloud-operations');
setupProcessHandlers();

async function createRollbackSnapshot(instancePath) {
    const instDir = path.dirname(instancePath);
    const folderName = path.basename(instancePath);
    const timestamp = Date.now();
    const rollbackTo = path.join(instDir, `${folderName}_rollback_${timestamp}`);
    try {
        await fs.promises.cp(instancePath, rollbackTo, { recursive: true });
        process.stderr.write(`[sync] Rollback créé : ${path.basename(rollbackTo)}\n`);
    } catch (e) {
        process.stderr.write(`[sync] Impossible de créer le rollback : ${e.message}\n`);
        return null;
    }
    try {
        const entries = await fs.promises.readdir(instDir);
        for (const entry of entries) {
            if (entry.startsWith(`${folderName}_rollback_`) && entry !== path.basename(rollbackTo)) {
                try { await fs.promises.rm(path.join(instDir, entry), { recursive: true, force: true }); } catch (_) { }
            }
        }
    } catch (_) { }
    return rollbackTo;
}
function cleanupRollback(rollbackPath) {
    if (!rollbackPath) return;
    try { fs.rmSync(rollbackPath, { recursive: true, force: true }); }
    catch (_) { }
}
async function syncAllInstances() {
    const args = process.argv.slice(2);
    const isList = args.includes('--list');
    if (!isList) {
        if (!acquireLock()) {
            console.log(JSON.stringify({
                type: 'ERROR',
                errorCode: 'ERR_ALREADY_RUNNING',
                message: 'ERR_ALREADY_RUNNING',
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
        const dataDir = getHorizonDataDir();
        const settingsPath = path.join(dataDir, 'horizon_settings.json');
        const syncInfoPath = path.join(dataDir, 'last_sync.json');
        const force = args.includes('--force');
        const isDelete = args.includes('--delete');
        const COMMANDS = new Set(['sync', 'upload', 'check', 'login', 'quota', 'rollback']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));
        let settings = { syncMode: 'SMART', maxRetries: 3, retryBaseDelay: 1500 };
        if (fs.existsSync(settingsPath)) {
            try { settings = { ...settings, ...JSON.parse(fs.readFileSync(settingsPath, 'utf8')) }; } catch (_) { }
        }
        const retryOpts = { maxRetries: settings.maxRetries || 3, baseDelay: settings.retryBaseDelay || 1500 };
        const provider = await getProvider(settings);
        if (!provider) {
            console.log(JSON.stringify({
                type: 'ERROR',
                errorCode: 'AUTH_EXPIRED',
                message: "Session expirée. Veuillez lier à nouveau votre compte depuis les paramètres.",
            }));
            return;
        }
        const cloudIndex = await getCloudIndexAndCleanDuplicates(provider, retryOpts, "[sync]");
        const metaCache = new Map();
        if (isList) {
            const metaFiles = Object.keys(cloudIndex).filter(n => n.startsWith('GensHorizon_Meta_'));
            const metaTasks = metaFiles.map((mName) => async () => {
                const instName = mName.replace('GensHorizon_Meta_', '').replace('.json', '');
                const localMetaPath = path.join(dataDir, `meta_${instName}.json`);
                let needsDownload = true;
                if (fs.existsSync(localMetaPath)) {
                    const localStat = fs.statSync(localMetaPath);
                    const cloudTime = new Date(cloudIndex[mName].modifiedTime).getTime();
                    if (localStat.mtime.getTime() >= cloudTime) {
                        needsDownload = false;
                        try { metaCache.set(instName, JSON.parse(fs.readFileSync(localMetaPath, 'utf8'))); } catch (_) { }
                    }
                }
                if (needsDownload) {
                    try {
                        const data = await provider.downloadJSON(cloudIndex[mName].id);
                        metaCache.set(instName, data);
                    } catch (_) { }
                }
            });
            await withConcurrency(6, metaTasks);
            const list = [...new Set(
                Object.keys(cloudIndex)
                    .filter(n => n.startsWith('GensHorizon_Backup_'))
                    .map(n => n.replace('GensHorizon_Backup_', '').replace('.zip', ''))
            )];
            const richList = list.map(instName => {
                const baseName = `GensHorizon_Backup_${instName}.zip`;
                const baseFile = cloudIndex[baseName];
                const deltaFiles = Object.keys(cloudIndex).filter(n => n.startsWith(`GensHorizon_Delta_${instName}_`) && n.endsWith('.zip'));
                const totalSizeBytes = deltaFiles.reduce((sum, n) => sum + (parseInt(cloudIndex[n].size, 10) || 0), 0) + (parseInt(baseFile?.size, 10) || 0);
                let realName = instName;
                if (isList) {
                    if (metaCache.has(instName) && metaCache.get(instName).realName) {
                        realName = metaCache.get(instName).realName;
                    }
                } else {
                    const localMetaPath = path.join(dataDir, `meta_${instName}.json`);
                    if (fs.existsSync(localMetaPath)) {
                        try {
                            const metaObj = JSON.parse(fs.readFileSync(localMetaPath, 'utf8'));
                            if (metaObj.realName) realName = metaObj.realName;
                        } catch (_) { }
                    }
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
            const safeTarget = getCanonicalName(targetInstance);
            const toDelete = Object.keys(cloudIndex).filter(n =>
                n === `GensHorizon_Backup_${safeTarget}.zip` ||
                n === `GensHorizon_Manifest_${safeTarget}.json` ||
                n === `GensHorizon_Meta_${safeTarget}.json` ||
                n.startsWith(`GensHorizon_Delta_${safeTarget}_`)
            );
            for (const n of toDelete) {
                await withRetry(() => provider.deleteFile(cloudIndex[n].id), { ...retryOpts, label: `deleteFile(${n})` });
            }
            const manifestPath = path.join(dataDir, `manifest_${safeTarget}.json`);
            if (fs.existsSync(manifestPath)) fs.unlinkSync(manifestPath);
            if (fs.existsSync(syncInfoPath)) {
                const syncState = readJsonSafe(syncInfoPath);
                delete syncState[safeTarget];
                await writeJsonAtomicAsync(syncInfoPath, syncState);
            }
            const metaPath = path.join(dataDir, `meta_${safeTarget}.json`);
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
            let isNewInstance = false;
            let tempBase = null;
            let tempDelta = null;
            try {
                const safeInst = getCanonicalName(inst);
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
                const targetPath = path.join(getInstancesFolder(), safeInst);
                const lastSync = syncState[safeInst] ? new Date(syncState[safeInst]).getTime() : 0;
                const baseTime = new Date(baseFile.modifiedTime).getTime();
                const deltaFiles = Object.keys(cloudIndex)
                    .filter(n => n.startsWith(`GensHorizon_Delta_${safeInst}_`) && n.endsWith('.zip'))
                    .map(n => {
                        const ts = parseInt(n.replace(`GensHorizon_Delta_${safeInst}_`, '').replace('.zip', ''), 10);
                        return { name: n, file: cloudIndex[n], ts };
                    })
                    .filter(d => !isNaN(d.ts))
                    .sort((a, b) => a.ts - b.ts);
                const pendingDeltas = deltaFiles.filter(d => d.ts > lastSync || force);
                const baseChanged = baseTime > lastSync || force;
                if (!baseChanged && pendingDeltas.length === 0) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${inst} est déjà à jour.` }));
                    continue;
                }
                if (!fs.existsSync(targetPath)) {
                    fs.mkdirSync(targetPath, { recursive: true });
                    isNewInstance = true;
                } else if (baseChanged || pendingDeltas.length > 0) {
                    rollbackPath = await createRollbackSnapshot(targetPath);
                }
                if (baseChanged) {
                    tempBase = path.join(dataDir, `download_base_${safeInst}.zip`);
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
                        for (const entry of fs.readdirSync(targetPath)) {
                            try { fs.rmSync(path.join(targetPath, entry), { recursive: true, force: true }); } catch (_) { }
                        }
                        await extractZip(tempBase, targetPath,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: pct, instance: inst }))
                        );
                    } finally {
                        try { if (fs.existsSync(tempBase)) fs.unlinkSync(tempBase); } catch (_) { }
                        unregisterTemp(tempBase);
                    }
                }
                for (const delta of pendingDeltas) {
                    tempDelta = path.join(dataDir, `download_delta_${safeInst}_${delta.ts}.zip`);
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
                        try { if (fs.existsSync(tempDelta)) fs.unlinkSync(tempDelta); } catch (_) { }
                        unregisterTemp(tempDelta);
                    }
                }
                const lastDelta = pendingDeltas.length > 0 ? pendingDeltas[pendingDeltas.length - 1] : null;
                syncState[safeInst] = lastDelta ? new Date(lastDelta.ts).toISOString() : baseFile.modifiedTime;
                await writeJsonAtomicAsync(syncInfoPath, syncState);
                cleanupRollback(rollbackPath);
                rollbackPath = null;
                try {
                    const newManifest = await generateManifest(targetPath);
                    await writeJsonAtomicAsync(path.join(dataDir, `manifest_${safeInst}.json`), newManifest);
                } catch (_) { }
                const deltasApplied = pendingDeltas.length;
                console.log(JSON.stringify({
                    type: 'SUCCESS',
                    instance: inst,
                    base: baseChanged,
                    deltas: deltasApplied,
                    message: baseChanged
                        ? `Base + ${deltasApplied} delta(s) appliqué(s).`
                        : `${deltasApplied} delta(s) appliqué(s).`,
                }));
            } catch (instErr) {
                if (isNewInstance) {
                    try {
                        const safeInst = getCanonicalName(inst);
                        const targetPath = path.join(getInstancesFolder(), safeInst);
                        if (fs.existsSync(targetPath)) try { fs.rmSync(targetPath, { recursive: true, force: true }); } catch (_) { }
                    } catch (_) { }
                }
                const rollbackMsg = rollbackPath
                    ? ` Un rollback est disponible (lance --rollback ${inst} pour restaurer).`
                    : '';
                console.log(JSON.stringify({
                    type: 'ERROR',
                    instance: inst,
                    message: instErr.message + rollbackMsg,
                    errorCode: instErr.errorCode || undefined,
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