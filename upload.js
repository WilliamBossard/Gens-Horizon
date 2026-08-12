'use strict';
const fs       = require('fs');
const { ZipArchive } = require('archiver'); // AUDIT-21 : import standard d'archiver (ZipArchive n'est pas un export nommé public)
const path     = require('path');
const { getInstancesFolder, scanInstances, getHorizonDataDir } = require('./paths');
const { generateManifest, compareManifests }   = require('./scanner');
const { getCloudIndexAndCleanDuplicates } = require('./cloud-operations');
const { getProvider }                          = require('./provider');
const { acquireLock, releaseLock, LOCK_FILE } = require('./lock');
const { withRetry }                            = require('./retry');
const {
    checkConnectivity,
    getCloudSettings,
    readJsonSafe,
    writeJsonAtomic,
    writeJsonAtomicAsync,
    getCanonicalName,
    registerTemp,
    unregisterTemp,
    setupProcessHandlers,
} = require('./utils');
const { PREFIX_BACKUP, PREFIX_DELTA, PREFIX_MANIFEST, PREFIX_META } = require('./cloud-constants');
setupProcessHandlers();
async function getFolderSize(dir, currentDepth = 0) {
    if (currentDepth > 20) return 0; 
    let total = 0;
    try {
        const entries = await fs.promises.readdir(dir, { withFileTypes: true });
        for (const entry of entries) {
            const fullPath = path.join(dir, entry.name);
            if (entry.isDirectory()) {
                total += await getFolderSize(fullPath, currentDepth + 1); 
            } else {
                try {
                    const stat = await fs.promises.stat(fullPath);
                    total += stat.size;
                } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
            }
        }
    } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
    return total;
}
async function createFullZip(folder, tempZip, inst) {
    const realTotal = await getFolderSize(folder);
    let lastPct = -1;
    return new Promise((resolve, reject) => {
        const output  = fs.createWriteStream(tempZip);
        const archive = new ZipArchive({ zlib: { level: 1 } }); // AUDIT-21
        archive.on('progress', (p) => {
            if (realTotal === 0) return;
            const pct = Math.min(100, Math.round(p.fs.processedBytes / realTotal * 100));
            if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                console.log(JSON.stringify({ type: 'PROGRESS', step: 'COMPRESSING', value: pct, instance: inst }));
                lastPct = pct;
            }
        });
        archive.on('warning', async (warn) => {
            if (warn.code === 'ENOENT') {
                process.stderr.write(`[upload] Fichier absent ignoré lors de la compression : ${warn.message}\n`);
            } else {
                output.destroy();
                try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
                reject(warn);
            }
        });
        archive.on('error', async err => { output.destroy(); try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); } reject(err); });
        output.on('close', resolve);
        output.on('error', async err => { archive.abort(); try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); } reject(err); });
        archive.pipe(output);
        archive.directory(folder, false, (data) => {
            return data;
        });
        archive.finalize();
    });
}
function createDeltaZip(folder, changed, deleted, tempZip, inst) {
    return new Promise(async (resolve, reject) => {
        const output = fs.createWriteStream(tempZip);
        const archive = new ZipArchive({ zlib: { level: 1 } }); // AUDIT-21
        output.on('close', resolve);
        archive.on('warning', async (warn) => {
            if (warn.code === 'ENOENT') {
                process.stderr.write(`[upload] Fichier absent ignoré lors de la compression delta : ${warn.message}\n`);
            } else {
                output.destroy();
                try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
                reject(warn);
            }
        });
        archive.on('error', async err => { output.destroy(); try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); } reject(err); });
        output.on('error', async err => { archive.abort(); try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); } reject(err); });
        archive.pipe(output);
        const deltaInfo = { deletedFiles: deleted, createdAt: new Date().toISOString() };
        archive.append(JSON.stringify(deltaInfo, null, 2), { name: '__delta__.json' });
        let lastPct = -1;
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
            if (!(await existsSafe(absPath))) continue; // skip silently — archiver gérerait ENOENT via 'warning'
            archive.file(absPath, { name: relPath });
        }
        archive.finalize();
    });
}
async function upload() {
    if (!(await acquireLock())) {
        console.log(JSON.stringify({
            type     : 'ERROR',
            errorCode: 'ERR_ALREADY_RUNNING',
            message  : 'ERR_ALREADY_RUNNING',
        }));
        process.exit(1);
    }
    const lockHeartbeat = setInterval(async () => {
        try { await fs.promises.utimes(LOCK_FILE, new Date(), new Date()); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
    }, 5 * 60_000);
    try {
        const online = await checkConnectivity();
        if (!online) {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible ou erreur réseau.' }));
            return;
        }
        const dataDir      = getHorizonDataDir();
        const settingsPath = path.join(dataDir, 'horizon_settings.json');
        const syncInfoPath = path.join(dataDir, 'last_sync.json');
        const args         = process.argv.slice(2);
        const force        = args.includes('--force');
        const COMMANDS     = new Set(['sync', 'upload', 'check', 'login', 'quota', 'rollback']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));
        const { sets: settings, retryOpts } = await getCloudSettings(settingsPath);
        const provider = await getProvider(settings);
        if (!provider) {
            console.log(JSON.stringify({
                type     : 'ERROR',
                errorCode: 'AUTH_EXPIRED',
                message  : "Session expirée. Veuillez lier à nouveau votre compte depuis les paramètres.",
            }));
            return;
        }
        let localInstances = [];
        if (targetInstance) {
            console.log(JSON.stringify({ type: 'PROGRESS', step: 'CHECKING', value: 0, instance: targetInstance }));
            const targetFolder = path.join(getInstancesFolder(), getCanonicalName(targetInstance));
            try { await fs.promises.access(targetFolder); } catch {
                console.log(JSON.stringify({ type: 'ERROR', message: `Instance ${targetInstance} introuvable localement.` }));
                return;
            }
            localInstances = [targetInstance];
        } else {
            localInstances = await scanInstances();
        }
        const cloudIndex = await getCloudIndexAndCleanDuplicates(provider, retryOpts, "[upload]");
        for (const name of Object.keys(cloudIndex)) {
            if (!name.startsWith(PREFIX_DELTA)) continue;
            const body  = name.replace(PREFIX_DELTA, '').replace('.zip', '');
            const parts = body.split('_');
            const ts    = parseInt(parts.pop(), 10);
            if (isNaN(ts)) continue;
            const instName  = parts.join('_');
            const baseName  = `${PREFIX_BACKUP}${instName}.zip`;
            const baseEntry = cloudIndex[baseName];
            if (!baseEntry) continue;
            const baseTime = new Date(baseEntry.modifiedTime).getTime();
            if (ts < baseTime) {
                try {
                    await withRetry(() => provider.deleteFile(cloudIndex[name].id), { ...retryOpts, label: `gc_orphanDelta(${name})` });
                    delete cloudIndex[name];
                    process.stderr.write(`[upload] GC : delta orphelin supprimé : ${name}\n`);
                } catch (e) {
                    process.stderr.write(`[upload] GC : échec suppression delta orphelin ${name}: ${e.message}\n`);
                }
            }
        }
        let syncState = await readJsonSafe(syncInfoPath);
        for (const inst of localInstances) {
            try {
                const folder = path.join(getInstancesFolder(), getCanonicalName(inst));
                const safeInst = getCanonicalName(inst); 
                const baseName     = `${PREFIX_BACKUP}${safeInst}.zip`;
                const manifestName = `${PREFIX_MANIFEST}${safeInst}.json`;
                const manifestPath = path.join(dataDir, `manifest_${safeInst}.json`);
                const oldManifest     = await readJsonSafe(manifestPath);
                const currentManifest = await generateManifest(folder, folder, oldManifest);
                const diff            = compareManifests(oldManifest, currentManifest);
                const hasBaseOnCloud = !!cloudIndex[baseName];
                const useSmartMode   = settings.syncMode === 'SMART';
                const metaName = `${PREFIX_META}${safeInst}.json`;
                let metaData = { iconData: "", loader: "vanilla", realName: inst };
                const instJsonPath = path.join(folder, 'instance.json');
                try {
                    const instObj = JSON.parse(await fs.promises.readFile(instJsonPath, 'utf8'));
                    metaData.loader = instObj.loader || 'vanilla';
                    if (instObj.name) metaData.realName = instObj.name;
                    if (instObj.icon && instObj.icon.startsWith('file://')) {
                        try {
                            const localIconPath = require('url').fileURLToPath(instObj.icon);
                            const resolvedIcon = path.resolve(localIconPath);
                            const resolvedFolder = path.resolve(folder);
                            const iconStat = await fs.promises.stat(resolvedIcon).catch(() => null);
                            if (iconStat && resolvedIcon.startsWith(resolvedFolder + path.sep) && iconStat.size < 512 * 1024) {
                                const rawExt = path.extname(resolvedIcon).toLowerCase().replace('.', '');
                                const ext = (rawExt === 'jpg') ? 'jpeg' : (rawExt || 'png');
                                const b64 = await fs.promises.readFile(resolvedIcon, { encoding: 'base64' });
                                metaData.iconData = `data:image/${ext};base64,${b64}`;
                            }
                        } catch(_) { metaData.iconData = ''; }
                    } else {
                        metaData.iconData = instObj.icon || '';
                    }
                } catch(_) { /* instance.json absent ou invalide — métadonnées par défaut */ }

                if (!metaData.iconData) {
                    const exts = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.bmp'];
                    for (const ext of exts) {
                        const iconPath = path.join(folder, 'icon' + ext);
                        const stat = await fs.promises.stat(iconPath).catch(() => null);
                        if (stat && stat.size < 512 * 1024) {
                            const b64 = await fs.promises.readFile(iconPath, { encoding: 'base64' });
                            const mimeExt = (ext === '.jpg') ? 'jpeg' : ext.replace('.', '');
                            metaData.iconData = `data:image/${mimeExt};base64,${b64}`;
                            break;
                        }
                    }
                }
                if (!diff.hasChanges && !force && hasBaseOnCloud) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `Aucun changement pour ${inst}, upload ignoré.` }));
                    continue;
                }
                if (!useSmartMode || force || !hasBaseOnCloud) {
                    const tempZip = path.join(dataDir, `temp_${safeInst}.zip`);
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
                        if (!useSmartMode || force) {
                            const deltasToDelete = Object.keys(cloudIndex).filter(n => n.startsWith(`${PREFIX_DELTA}${safeInst}_`));
                            for (const dName of deltasToDelete) {
                                try {
                                    await withRetry(() => provider.deleteFile(cloudIndex[dName].id), { ...retryOpts, label: `deleteFile(${dName})` });
                                } catch (e) {
                                    process.stderr.write(`[WARN] Échec suppression delta orphelin ${dName}: ${e.message}\n`);
                                }
                            }
                        }
                        syncState[safeInst] = result?.modifiedTime || new Date().toISOString();
                        await writeJsonAtomicAsync(syncInfoPath, syncState);
                        await writeJsonAtomicAsync(manifestPath, currentManifest);
                        console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'FULL' }));
                    } finally {
                        try { if (await existsSafe(tempZip)) await fs.promises.unlink(tempZip); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
                        unregisterTemp(tempZip);
                    }
                    continue;
                }
                const DELTA_THRESHOLD = settings.deltaCleanupThreshold || 10;
                const existingDeltas  = Object.keys(cloudIndex).filter(n => n.startsWith(`${PREFIX_DELTA}${safeInst}_`));
                if (existingDeltas.length >= DELTA_THRESHOLD) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${existingDeltas.length} delta(s) — repack complet (seuil: ${DELTA_THRESHOLD}).` }));
                    const tempZipRepack = path.join(dataDir, `temp_${safeInst}.zip`);
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
                        for (const dName of existingDeltas) {
                            try {
                                await withRetry(() => provider.deleteFile(cloudIndex[dName].id), { ...retryOpts, label: `deleteFile(${dName})` });
                            } catch (e) {
                                process.stderr.write(`[WARN] Échec suppression delta repack ${dName}: ${e.message}\n`);
                            }
                        }
                        syncState[safeInst] = repackResult?.modifiedTime || new Date().toISOString();
                        await writeJsonAtomicAsync(syncInfoPath, syncState);
                        await writeJsonAtomicAsync(manifestPath, currentManifest);
                        console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'REPACK' }));
                    } finally {
                        try { if (await existsSafe(tempZipRepack)) await fs.promises.unlink(tempZipRepack); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
                        unregisterTemp(tempZipRepack);
                    }
                    continue;
                }
                const changedFiles = [...diff.added, ...diff.modified];
                const deletedFiles = diff.deleted;
                let timestamp = Date.now();
                let maxTs = 0;
                for (const n of existingDeltas) {
                    const ts = parseInt(n.replace(`${PREFIX_DELTA}${safeInst}_`, '').replace('.zip', ''), 10);
                    if (!isNaN(ts) && ts > maxTs) maxTs = ts;
                }
                if (timestamp <= maxTs) {
                    timestamp = maxTs + 1000;
                }
                const deltaName    = `${PREFIX_DELTA}${safeInst}_${timestamp}.zip`;
                const tempDelta    = path.join(dataDir, `delta_${safeInst}_${timestamp}.zip`);
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
                    syncState[safeInst] = new Date(timestamp).toISOString();
                    await writeJsonAtomicAsync(syncInfoPath, syncState);
                    await writeJsonAtomicAsync(manifestPath, currentManifest);
                    const summary = `+${diff.added.length} ajouté(s), ~${diff.modified.length} modifié(s), -${diff.deleted.length} supprimé(s)`;
                    console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'SMART', summary }));
                } finally {
                    try { if (await existsSafe(tempDelta)) await fs.promises.unlink(tempDelta); } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[upload.js] Erreur silencieuse interceptée:', _.message || _); }
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
        clearInterval(lockHeartbeat);
        releaseLock();
    }
}
upload();
