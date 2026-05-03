'use strict';

const fs      = require('fs');
const fsP     = fs.promises;
const archiver = require('archiver');
const AdmZip  = require('adm-zip');
const path    = require('path');
const dns     = require('dns').promises;

const { getInstancesFolder, scanInstances } = require('./paths');
const { generateManifest, compareManifests } = require('./scanner');
const { getProvider }                        = require('./provider');

async function getFolderSize(dir) {
    let total = 0;
    try {
        const entries = await fsP.readdir(dir, { withFileTypes: true });
        await Promise.all(entries.map(async (entry) => {
            const fullPath = path.join(dir, entry.name);
            if (entry.isDirectory()) {
                total += await getFolderSize(fullPath);
            } else {
                try {
                    const stat = await fsP.stat(fullPath);
                    total += stat.size;
                } catch (_) {}
            }
        }));
    } catch (_) {}
    return total;
}

function createFullZip(folder, tempZip, inst) {
    return getFolderSize(folder).then(realTotal => {
        let lastPct = -1;
        return new Promise((resolve, reject) => {
            const output  = fs.createWriteStream(tempZip);
            const archive = archiver('zip', { zlib: { level: 6 } });

            archive.on('progress', (p) => {
                if (realTotal === 0) return;
                let pct = Math.min(100, Math.round(p.fs.processedBytes / realTotal * 100));
                if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                    console.log(JSON.stringify({ type: 'PROGRESS', step: 'COMPRESSING', value: pct, instance: inst }));
                    lastPct = pct;
                }
            });
            archive.on('error', err => { output.destroy(); try { fs.unlinkSync(tempZip); } catch (_) {} reject(err); });
            output.on('close', resolve);
            output.on('error', err => { try { fs.unlinkSync(tempZip); } catch (_) {} reject(err); });

            archive.pipe(output);
            archive.directory(folder, false);
            archive.finalize();
        });
    });
}

async function createDeltaZip(folder, changed, deleted, tempZip, inst) {
    const zip = new AdmZip();

    const deltaInfo = { deletedFiles: deleted, createdAt: new Date().toISOString() };
    zip.addFile('__delta__.json', Buffer.from(JSON.stringify(deltaInfo, null, 2)));

    let done = 0;
    let lastPct = -1;

    for (const relPath of changed) {
        const absPath = path.join(folder, relPath.replace(/\//g, path.sep));
        if (!fs.existsSync(absPath)) continue;
        const zipDir = path.posix.dirname(relPath) === '.' ? '' : path.posix.dirname(relPath);
        zip.addLocalFile(absPath, zipDir);

        done++;
        if (changed.length > 0) {
            const pct = Math.min(100, Math.round(done / changed.length * 100));
            if (pct !== lastPct && (pct >= lastPct + 5 || pct === 100)) {
                console.log(JSON.stringify({ type: 'PROGRESS', step: 'COMPRESSING', value: pct, instance: inst }));
                lastPct = pct;
            }
        }
    }

    await new Promise((resolve, reject) => zip.writeZip(tempZip, err => err ? reject(err) : resolve()));
}

// CORRIGÉ : teste plusieurs hôtes pour éviter les faux "offline" sur réseaux qui bloquent google.com
async function checkConnectivity() {
    const hosts = ['1.1.1.1', 'google.com', 'microsoft.com'];
    for (const host of hosts) {
        try { await dns.lookup(host); return true; } catch (_) {}
    }
    return false;
}

async function upload() {
    try {
        const online = await checkConnectivity();
        if (!online) {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible ou erreur réseau.' }));
            return;
        }

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');

        const args           = process.argv.slice(2);
        const force          = args.includes('--force');
        const COMMANDS       = new Set(['sync', 'upload', 'check', 'login']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        let settings = { syncMode: 'SMART', autoSync: true, autoUpload: true };
        if (fs.existsSync(settingsPath)) {
            try { settings = { ...settings, ...JSON.parse(fs.readFileSync(settingsPath, 'utf8')) }; } catch (_) {}
        }
    
        const provider = await getProvider(settings, cwd);
        if (!provider) {
            console.log(JSON.stringify({ type: 'ERROR', message: 'Compte non lié. Lance --login d\'abord.' }));
            return;
        }

        let localInstances = []; 

        if (targetInstance) {
            console.log(JSON.stringify({ type: 'PROGRESS', step: 'CHECKING', value: 0, instance: targetInstance }));
            const targetFolder = path.join(getInstancesFolder(), targetInstance);
            if (!fs.existsSync(targetFolder)) {
                console.log(JSON.stringify({ type: 'ERROR', message: `Instance ${targetInstance} introuvable localement.` }));
                return;
            }
            localInstances = [targetInstance];
        } else {
            localInstances = scanInstances();
        }

        const cloudFiles = await provider.listFiles('GensHorizon_');
        const cloudIndex = {};
        for (const f of cloudFiles) {
            cloudIndex[f.name] = f; 
        }

        for (const inst of localInstances) {
            try {
                const folder       = path.join(getInstancesFolder(), inst);
                const manifestPath = path.join(cwd, `manifest_${inst}.json`);
                const baseName     = `GensHorizon_Backup_${inst}.zip`;
                const manifestName = `GensHorizon_Manifest_${inst}.json`;
                const oldManifest = fs.existsSync(manifestPath)
                    ? JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
                    : {};

                const currentManifest = await generateManifest(folder);
                const diff = compareManifests(oldManifest, currentManifest);

                const hasBaseOnCloud = !!cloudIndex[baseName];
                const useSmartMode   = settings.syncMode === 'SMART';

                if (!diff.hasChanges && !force && hasBaseOnCloud) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `Aucun changement pour ${inst}, upload ignoré.` }));
                    continue;
                }

                if (!useSmartMode || force || !hasBaseOnCloud) {
                    if (!useSmartMode || force) {
                        const deltasToDelete = Object.keys(cloudIndex).filter(n => n.startsWith(`GensHorizon_Delta_${inst}_`));
                        for (const dName of deltasToDelete) {
                            await provider.deleteFile(cloudIndex[dName].id);
                            console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `Delta supprimé : ${dName}` }));
                        }
                    }

                    const tempZip = path.join(cwd, `temp_${inst}.zip`);
                    await createFullZip(folder, tempZip, inst);

                    const existingBase = hasBaseOnCloud ? cloudIndex[baseName].id : null;
                    console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: 0, instance: inst }));
                    const result = await provider.uploadZip(
                        baseName, tempZip, existingBase,
                        (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: pct, instance: inst }))
                    );
                    try { fs.unlinkSync(tempZip); } catch (_) {}

                    await provider.uploadJSON(manifestName, currentManifest);
                    const syncState = fs.existsSync(syncInfoPath) ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8')) : {};
                    syncState[inst] = result.modifiedTime;
                    fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));
                    fs.writeFileSync(manifestPath, JSON.stringify(currentManifest, null, 2));
                    console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'FULL' }));
                    continue;
                }

                const DELTA_THRESHOLD = settings.deltaCleanupThreshold || 10;
                const existingDeltas  = Object.keys(cloudIndex).filter(n => n.startsWith(`GensHorizon_Delta_${inst}_`));
                if (existingDeltas.length >= DELTA_THRESHOLD) {
                    console.log(JSON.stringify({ type: 'INFO', instance: inst, message: `${existingDeltas.length} delta(s) accumulé(s) — repack complet automatique (seuil: ${DELTA_THRESHOLD}).` }));
                    for (const dName of existingDeltas) await provider.deleteFile(cloudIndex[dName].id);
                    const tempZipRepack = path.join(cwd, `temp_${inst}.zip`);
                    try {
                        await createFullZip(folder, tempZipRepack, inst);
                        const existingBase = cloudIndex[baseName] ? cloudIndex[baseName].id : null;
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: 0, instance: inst }));
                        const repackResult = await provider.uploadZip(baseName, tempZipRepack, existingBase,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: pct, instance: inst })));
                        await provider.uploadJSON(manifestName, currentManifest);
                        const syncStateR = fs.existsSync(syncInfoPath) ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8')) : {};
                        syncStateR[inst] = repackResult?.modifiedTime || new Date().toISOString();
                        fs.writeFileSync(syncInfoPath, JSON.stringify(syncStateR, null, 2));
                        fs.writeFileSync(manifestPath, JSON.stringify(currentManifest, null, 2));
                        console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'REPACK' }));
                    } finally {
                        try { if (fs.existsSync(tempZipRepack)) fs.unlinkSync(tempZipRepack); } catch(_) {}
                    }
                    continue;
                }

                const changedFiles = [...diff.added, ...diff.modified];
                const deletedFiles = diff.deleted;

                const timestamp = Date.now();
                const deltaName = `GensHorizon_Delta_${inst}_${timestamp}.zip`;
                const tempDelta = path.join(cwd, `delta_${inst}_${timestamp}.zip`);

                await createDeltaZip(folder, changedFiles, deletedFiles, tempDelta, inst);

                console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: 0, instance: inst }));
                await provider.uploadZip(
                    deltaName, tempDelta, null,
                    (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'UPLOADING', value: pct, instance: inst }))
                );
                try { fs.unlinkSync(tempDelta); } catch (_) {}

                await provider.uploadJSON(manifestName, currentManifest);
                const syncState = fs.existsSync(syncInfoPath) ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8')) : {};
                syncState[inst] = new Date().toISOString();
                fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));
                fs.writeFileSync(manifestPath, JSON.stringify(currentManifest, null, 2));

                const summary = `+${diff.added.length} ajouté(s), ~${diff.modified.length} modifié(s), -${diff.deleted.length} supprimé(s)`;
                console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'SMART', summary }));

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
    }
}

upload();
