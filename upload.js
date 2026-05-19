'use strict';

const fs       = require('fs');
const archiver = require('archiver');
const path     = require('path');

const { getInstancesFolder, scanInstances }    = require('./paths');
const { generateManifest, compareManifests }   = require('./scanner');
const { getProvider }                          = require('./provider');
const { acquireLock, releaseLock }             = require('./lock');
const { withRetry }                            = require('./retry');
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

function getFolderSize(dir) {
    let total = 0;
    try {
        const entries = fs.readdirSync(dir, { withFileTypes: true });
        for (const entry of entries) {
            const fullPath = path.join(dir, entry.name);
            if (entry.isDirectory()) {
                total += getFolderSize(fullPath);
            } else {
                try { total += fs.statSync(fullPath).size; } catch (_) {}
            }
        }
    } catch (_) {}
    return total;
}

function createFullZip(folder, tempZip, inst) {
    const realTotal = getFolderSize(folder);
    let lastPct = -1;
    return new Promise((resolve, reject) => {
        const output  = fs.createWriteStream(tempZip);
        const archive = archiver('zip', { zlib: { level: 6 } });

        archive.on('progress', (p) => {
            if (realTotal === 0) return;
            const pct = Math.min(100, Math.round(p.fs.processedBytes / realTotal * 100));
            if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                console.log(JSON.stringify({ type: 'PROGRESS', step: 'COMPRESSING', value: pct, instance: inst }));
                lastPct = pct;
            }
        });
        archive.on('error', err => { output.destroy(); try { fs.unlinkSync(tempZip); } catch (_) {} reject(err); });
        output.on('close', resolve);
        output.on('error', err => { archive.abort(); try { fs.unlinkSync(tempZip); } catch (_) {} reject(err); });

        archive.pipe(output);
        archive.directory(folder, false);
        archive.finalize();
    });
}

function createDeltaZip(folder, changed, deleted, tempZip, inst) {
    return new Promise((resolve, reject) => {
        const output = fs.createWriteStream(tempZip);
        const archive = archiver('zip', { zlib: { level: 6 } });

        output.on('close', resolve);
        archive.on('error', err => { output.destroy(); try { fs.unlinkSync(tempZip); } catch (_) {} reject(err); });
        output.on('error', err => { archive.abort(); try { fs.unlinkSync(tempZip); } catch (_) {} reject(err); });

        archive.pipe(output);

        const deltaInfo = { deletedFiles: deleted, createdAt: new Date().toISOString() };
        archive.append(JSON.stringify(deltaInfo, null, 2), { name: '__delta__.json' });

        let done = 0, lastPct = -1;
        archive.on('progress', (p) => {
            if (changed.length === 0) return;
            const pct = Math.min(100, Math.round((p.entries.processed / changed.length) * 100));
            if (pct !== lastPct && (pct >= lastPct + 5 || pct === 100)) {
                console.log(JSON.stringify({ type: 'PROGRESS', step: 'COMPRESSING', value: pct, instance: inst }));
                lastPct = pct;
            }
        });

        for (const relPath of changed) {
            const absPath = path.join(folder, relPath.replace(/\//g, path.sep));
            if (!fs.existsSync(absPath)) continue;
            archive.file(absPath, { name: relPath });
        }
        
        archive.finalize();
        archive.finalize();
    });
}

async function upload() {
    if (!acquireLock()) {
        console.log(JSON.stringify({
            type     : 'ERROR',
            errorCode: 'ERR_ALREADY_RUNNING',
            message  : 'ERR_ALREADY_RUNNING',
        }));
        process.exit(1);
    }

    try {
        const online = await checkConnectivity();
        if (!online) {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible ou erreur réseau.' }));
            return;
        }

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');
        const args         = process.argv.slice(2);
        const force        = args.includes('--force');
        const COMMANDS     = new Set(['sync', 'upload', 'check', 'login', 'quota', 'rollback']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        let settings = { syncMode: 'SMART', autoSync: true, autoUpload: true, maxRetries: 3, retryBaseDelay: 1500 };
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

        let localInstances = [];
        if (targetInstance) {
            console.log(JSON.stringify({ type: 'PROGRESS', step: 'CHECKING', value: 0, instance: targetInstance }));
            const targetFolder = path.join(getInstancesFolder(), getFolderFromName(targetInstance));
            if (!fs.existsSync(targetFolder)) {
                console.log(JSON.stringify({ type: 'ERROR', message: `Instance ${targetInstance} introuvable localement.` }));
                return;
            }
            localInstances = [targetInstance];
        } else {
            localInstances = scanInstances();
        }

        const cloudFiles = await withRetry(() => provider.listFiles('GensHorizon_'), { ...retryOpts, label: 'listFiles' });
        const cloudIndex = {};
        for (const f of cloudFiles) cloudIndex[f.name] = f;

        let syncState = readJsonSafe(syncInfoPath);

        for (const inst of localInstances) {
            try {
                const folder = path.join(getInstancesFolder(), getFolderFromName(inst));
                const safeInst = sanitizeInstanceName(inst);
                const baseName     = `GensHorizon_Backup_${safeInst}.zip`;
                const manifestName = `GensHorizon_Manifest_${safeInst}.json`;
                const manifestPath = path.join(cwd, `manifest_${safeInst}.json`);

                const oldManifest     = readJsonSafe(manifestPath);
                const currentManifest = await generateManifest(folder);
                const diff            = compareManifests(oldManifest, currentManifest);

                const hasBaseOnCloud = !!cloudIndex[baseName];
                const useSmartMode   = settings.syncMode === 'SMART';

                const metaName = `GensHorizon_Meta_${safeInst}.json`;
                let metaData = { iconData: "", loader: "vanilla", realName: inst };
                const instJsonPath = path.join(folder, 'instance.json');
                if (fs.existsSync(instJsonPath)) {
                    try {
                        const instObj = JSON.parse(fs.readFileSync(instJsonPath, 'utf8'));
                        metaData.loader = instObj.loader || "vanilla";
                        if (instObj.name) metaData.realName = instObj.name; 
                        if (instObj.icon && instObj.icon.startsWith('file://')) {
                            try {
                                const localIconPath = require('url').fileURLToPath(instObj.icon);
                                const resolvedIcon = path.resolve(localIconPath);
                                const resolvedFolder = path.resolve(folder);
                                
                                if (fs.existsSync(resolvedIcon) && resolvedIcon.startsWith(resolvedFolder + path.sep) && fs.statSync(resolvedIcon).size < 512 * 1024) {
                                    const rawExt = path.extname(resolvedIcon).toLowerCase().replace('.', '');
                                    const ext = (rawExt === 'jpg') ? 'jpeg' : (rawExt || 'png');
                                    const b64 = fs.readFileSync(resolvedIcon, 'base64');
                                    metaData.iconData = `data:image/${ext};base64,${b64}`;
                                }
                            } catch(_) { metaData.iconData = ""; }
                        } else {
                            metaData.iconData = instObj.icon || "";
                        }
                    } catch(_) {}
                }

                if (!diff.hasChanges && !force && hasBaseOnCloud) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `Aucun changement pour ${inst}, upload ignoré.` }));
                    continue;
                }

                if (!useSmartMode || force || !hasBaseOnCloud) {
                    if (!useSmartMode || force) {
                        const deltasToDelete = Object.keys(cloudIndex).filter(n => n.startsWith(`GensHorizon_Delta_${safeInst}_`));
                        for (const dName of deltasToDelete) {
                            await withRetry(() => provider.deleteFile(cloudIndex[dName].id), { ...retryOpts, label: `deleteFile(${dName})` });
                            console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `Delta supprimé : ${dName}` }));
                        }
                    }

                    const tempZip = path.join(cwd, `temp_${safeInst}.zip`);
                    registerTemp(tempZip);
                    try {
                        await createFullZip(folder, tempZip, inst);
                        const existingBase = hasBaseOnCloud ? cloudIndex[baseName].id : null;
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: 0, instance: inst }));

                        const result = await withRetry(
                            () => provider.uploadZip(
                                baseName, tempZip, existingBase,
                                (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: pct, instance: inst }))
                            ),
                            { ...retryOpts, label: `uploadZip(${inst})` }
                        );

                        await withRetry(
                            () => provider.uploadJSON(manifestName, currentManifest, cloudIndex[manifestName]?.id),
                            { ...retryOpts, label: `uploadManifest(${inst})` }
                        );
                        await withRetry(
                            () => provider.uploadJSON(metaName, metaData, cloudIndex[metaName]?.id),
                            { ...retryOpts, label: `uploadMeta(${inst})` }
                        );

                        syncState[safeInst] = result?.modifiedTime || new Date().toISOString();
                        writeJsonAtomic(syncInfoPath, syncState);
                        writeJsonAtomic(manifestPath, currentManifest);
                        console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'FULL' }));
                    } finally {
                        try { if (fs.existsSync(tempZip)) fs.unlinkSync(tempZip); } catch (_) {}
                        unregisterTemp(tempZip);
                    }
                    continue;
                }

                const DELTA_THRESHOLD = settings.deltaCleanupThreshold || 10;
                const existingDeltas  = Object.keys(cloudIndex).filter(n => n.startsWith(`GensHorizon_Delta_${safeInst}_`));
                if (existingDeltas.length >= DELTA_THRESHOLD) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${existingDeltas.length} delta(s) — repack complet (seuil: ${DELTA_THRESHOLD}).` }));
                    for (const dName of existingDeltas) {
                        await withRetry(() => provider.deleteFile(cloudIndex[dName].id), { ...retryOpts, label: `deleteFile(${dName})` });
                    }

                    const tempZipRepack = path.join(cwd, `temp_${safeInst}.zip`);
                    registerTemp(tempZipRepack);
                    try {
                        await createFullZip(folder, tempZipRepack, inst);
                        const existingBase = cloudIndex[baseName] ? cloudIndex[baseName].id : null;
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: 0, instance: inst }));

                        const repackResult = await withRetry(
                            () => provider.uploadZip(
                                baseName, tempZipRepack, existingBase,
                                (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: pct, instance: inst }))
                            ),
                            { ...retryOpts, label: `uploadZipRepack(${inst})` }
                        );

                        await withRetry(
                            () => provider.uploadJSON(manifestName, currentManifest, cloudIndex[manifestName]?.id),
                            { ...retryOpts, label: `uploadManifest(${inst})` }
                        );
                        await withRetry(
                            () => provider.uploadJSON(metaName, metaData, cloudIndex[metaName]?.id),
                            { ...retryOpts, label: `uploadMeta(${inst})` }
                        );

                        syncState[safeInst] = repackResult?.modifiedTime || new Date().toISOString();
                        writeJsonAtomic(syncInfoPath, syncState);
                        writeJsonAtomic(manifestPath, currentManifest);
                        console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'REPACK' }));
                    } finally {
                        try { if (fs.existsSync(tempZipRepack)) fs.unlinkSync(tempZipRepack); } catch (_) {}
                        unregisterTemp(tempZipRepack);
                    }
                    continue;
                }

                const changedFiles = [...diff.added, ...diff.modified];
                const deletedFiles = diff.deleted;
                const timestamp    = Date.now();
                const deltaName    = `GensHorizon_Delta_${safeInst}_${timestamp}.zip`;
                const tempDelta    = path.join(cwd, `delta_${safeInst}_${timestamp}.zip`);

                registerTemp(tempDelta);
                try {
                    await createDeltaZip(folder, changedFiles, deletedFiles, tempDelta, inst);
                    console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: 0, instance: inst }));

                    const deltaResult = await withRetry(
                        () => provider.uploadZip(
                            deltaName, tempDelta, null,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: pct, instance: inst }))
                        ),
                        { ...retryOpts, label: `uploadDelta(${inst})` }
                    );

                    await withRetry(
                        () => provider.uploadJSON(manifestName, currentManifest, cloudIndex[manifestName]?.id),
                        { ...retryOpts, label: `uploadManifest(${inst})` }
                    );
                    await withRetry(
                        () => provider.uploadJSON(metaName, metaData, cloudIndex[metaName]?.id),
                        { ...retryOpts, label: `uploadMeta(${inst})` }
                    );

                    syncState[safeInst] = deltaResult?.modifiedTime || new Date().toISOString();
                    writeJsonAtomic(syncInfoPath, syncState);
                    writeJsonAtomic(manifestPath, currentManifest);
                    const summary = `+${diff.added.length} ajouté(s), ~${diff.modified.length} modifié(s), -${diff.deleted.length} supprimé(s)`;
                    console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'SMART', summary }));
                } finally {
                    try { if (fs.existsSync(tempDelta)) fs.unlinkSync(tempDelta); } catch (_) {}
                    unregisterTemp(tempDelta);
                }

            } catch (instErr) {
                console.log(JSON.stringify({ type: 'ERROR', instance: inst, message: instErr.message }));
            }
        }

    } catch (e) {
        if (e.message && (e.message.includes('invalid_grant') || e.message.includes('invalid_token'))) {
            console.log(JSON.stringify({ type: 'ERROR', message: 'Session expirée. Veuillez ré-associer votre compte.' }));
        } else if (e.code === 'ENOTFOUND' || e.code === 'EAI_AGAIN' || e.code === 'ECONNREFUSED') {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible ou erreur réseau.' }));
        } else {
            console.log(JSON.stringify({ type: 'ERROR', message: e.message }));
        }
    } finally {
        releaseLock();
    }
}

upload();