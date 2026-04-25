/**
 * sync.js
 * Synchronisation Cloud → Local des instances Minecraft.
 *
 * ── Fonctionnement ────────────────────────────────────────────────────────────
 * 1. Télécharge le base ZIP (GensHorizon_Backup_{inst}.zip) s'il est plus récent.
 * 2. Cherche tous les delta ZIPs (GensHorizon_Delta_{inst}_{timestamp}.zip) qui
 * sont plus récents que le dernier sync local, et les applique dans l'ordre
 * chronologique (timestamp croissant).
 * 3. Pour chaque delta : extrait les fichiers, puis supprime ceux listés dans
 * __delta__.json.
 * 4. Met à jour last_sync.json.
 *
 * ── Arguments CLI ─────────────────────────────────────────────────────────────
 * --sync [instanceName]    Synchronise une instance spécifique ou toutes
 * --force                  Force le re-téléchargement même si déjà à jour
 * --list                   Liste les instances disponibles sur le Cloud
 * --delete instanceName    Supprime une instance du Cloud
 * --provider=xxx           google | dropbox | onedrive
 */

'use strict';

const fs     = require('fs');
const AdmZip = require('adm-zip');
const path   = require('path');
const dns    = require('dns').promises;

const { getInstancesFolder } = require('./paths');
const { getProvider }        = require('./provider');

/**
 * Extrait le ZIP complet avec un rapport de progression
 * @param {string} zipPath
 * @param {string} targetPath
 * @param {function} onProgress
 */
function extractZip(zipPath, targetPath, onProgress) {
    const zip = new AdmZip(zipPath);
    const entries = zip.getEntries();
    const total = entries.length;
    let done = 0;
    let lastPct = -1;

    for (const entry of entries) {
        if (entry.isDirectory) {
            fs.mkdirSync(path.join(targetPath, entry.entryName), { recursive: true });
        } else {
            const dest = path.join(targetPath, entry.entryName);
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            fs.writeFileSync(dest, zip.readFile(entry));
        }
        done++;
        if (onProgress && total > 0) {
            const pct = Math.floor((done / total) * 100);
            if (pct !== lastPct && (pct >= lastPct + 3 || pct === 100)) {
                onProgress(pct);
                lastPct = pct;
            }
        }
    }
}

/** * Applique un delta ZIP avec rapport de progression
 * @param {string} deltaZipPath
 * @param {string} targetPath
 * @param {function} onProgress
 */
function applyDelta(deltaZipPath, targetPath, onProgress) {
    const zip      = new AdmZip(deltaZipPath);
    const entries  = zip.getEntries();
    const total    = entries.length;
    let done       = 0;
    let lastPct    = -1;

    const deltaEntry = entries.find(e => e.entryName === '__delta__.json');
    const deltaInfo  = deltaEntry ? JSON.parse(zip.readAsText(deltaEntry)) : { deletedFiles: [] };

    for (const entry of entries) {
        if (entry.entryName === '__delta__.json') {
            done++;
            continue;
        }
        if (entry.isDirectory) {
            fs.mkdirSync(path.join(targetPath, entry.entryName), { recursive: true });
        } else {
            const dest = path.join(targetPath, entry.entryName);
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            fs.writeFileSync(dest, zip.readFile(entry));
        }
        done++;
        if (onProgress && total > 0) {
            const pct = Math.floor((done / total) * 100);
            if (pct !== lastPct && (pct >= lastPct + 3 || pct === 100)) {
                onProgress(pct);
                lastPct = pct;
            }
        }
    }

for (const relPath of (deltaInfo.deletedFiles || [])) {
        const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
        if (!path.resolve(absPath).startsWith(path.resolve(targetPath))) {
            console.log(`[ALERTE SÉCURITÉ] Suppression ignorée (Hors de l'instance) : ${absPath}`);
            continue;
        }
        try { if (fs.existsSync(absPath)) fs.unlinkSync(absPath); } catch (_) {}
    }
}

async function syncAllInstances() {
    try {
        await dns.lookup('google.com');

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');

        const args           = process.argv.slice(2);
        const force          = args.includes('--force');
        const isList         = args.includes('--list');
        const isDelete       = args.includes('--delete');
        const COMMANDS       = new Set(['sync', 'upload', 'check', 'login']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        let settings = { syncMode: 'SMART' };
        if (fs.existsSync(settingsPath)) {
            try { settings = { ...settings, ...JSON.parse(fs.readFileSync(settingsPath, 'utf8')) }; } catch (_) {}
        }

        const provider = await getProvider(settings, cwd);
        if (!provider) {
            console.log(JSON.stringify({ type: 'ERROR', message: 'Compte non lié. Lance --login d\'abord.' }));
            return;
        }

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
                const deltaFiles = Object.keys(cloudIndex)
                    .filter(n => n.startsWith(`GensHorizon_Delta_${instName}_`) && n.endsWith('.zip'));
                const totalSizeBytes = deltaFiles.reduce((sum, n) => sum + (parseInt(cloudIndex[n].size, 10) || 0), 0)
                    + (parseInt(baseFile?.size, 10) || 0);
                return {
                    name       : instName,
                    deltaCount : deltaFiles.length,
                    sizeBytes  : totalSizeBytes,
                    lastBackup : baseFile?.modifiedTime || null,
                };
            });

            console.log(JSON.stringify({ type: 'CLOUD_LIST', data: list, richData: richList }));
            return;
        }

        if (isDelete && targetInstance) {
            const toDelete = Object.keys(cloudIndex).filter(n =>
                n === `GensHorizon_Backup_${targetInstance}.zip`      ||
                n === `GensHorizon_Manifest_${targetInstance}.json`   ||
                n.startsWith(`GensHorizon_Delta_${targetInstance}_`)
            );
            for (const n of toDelete) await provider.deleteFile(cloudIndex[n].id);

            const manifestPath = path.join(cwd, `manifest_${targetInstance}.json`);
            if (fs.existsSync(manifestPath)) fs.unlinkSync(manifestPath);
            if (fs.existsSync(syncInfoPath)) {
                const syncState = JSON.parse(fs.readFileSync(syncInfoPath, 'utf8'));
                delete syncState[targetInstance];
                fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));
            }

            console.log(JSON.stringify({ type: 'SUCCESS', instance: targetInstance, message: 'Supprimé du cloud.' }));
            return;
        }

        let syncState = fs.existsSync(syncInfoPath)
            ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8'))
            : {};

        const instancesToSync = targetInstance
            ? [targetInstance]
            : [...new Set(
                Object.keys(cloudIndex)
                    .filter(n => n.startsWith('GensHorizon_Backup_'))
                    .map(n => n.replace('GensHorizon_Backup_', '').replace('.zip', ''))
              )];

        for (const inst of instancesToSync) {
            try {
                if (targetInstance) {
                    console.log(JSON.stringify({ type: 'PROGRESS', step: 'CHECKING', value: 0, instance: inst }));
                }

                const baseName    = `GensHorizon_Backup_${inst}.zip`;
                const baseFile    = cloudIndex[baseName];

                if (!baseFile) {
                    console.log(JSON.stringify({
                        type: 'INFO', instance: inst,
                        message: `${inst} n'existe pas encore sur le Cloud.`
                    }));
                    continue;
                }

                const targetPath  = path.join(getInstancesFolder(), inst);
                const lastSync    = syncState[inst] ? new Date(syncState[inst]).getTime() : 0;
                const baseTime    = new Date(baseFile.modifiedTime).getTime();

                const deltaFiles = Object.keys(cloudIndex)
                    .filter(n => n.startsWith(`GensHorizon_Delta_${inst}_`) && n.endsWith('.zip'))
                    .map(n => {
                        const ts = parseInt(n.replace(`GensHorizon_Delta_${inst}_`, '').replace('.zip', ''), 10);
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

                if (!fs.existsSync(targetPath)) fs.mkdirSync(targetPath, { recursive: true });

                if (baseChanged) {
                    const tempBase = path.join(cwd, `download_base_${inst}.zip`);
                    try {
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'DOWNLOADING', value: 0, instance: inst }));
                        await provider.downloadFile(
                            baseFile.id,
                            tempBase,
                            (pct) => console.log(JSON.stringify({ type: 'PROGRESS', step: 'DOWNLOADING', value: pct, instance: inst })),
                            parseInt(baseFile.size, 10) || 0
                        );
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: 0, instance: inst }));
                        extractZip(tempBase, targetPath, (pct) => {
                            console.log(JSON.stringify({ type: 'PROGRESS', step: 'EXTRACTING', value: pct, instance: inst }));
                        });
                    } finally {
                        try { if (fs.existsSync(tempBase)) fs.unlinkSync(tempBase); } catch (_) {}
                    }
                }

                for (const delta of pendingDeltas) {
                    const tempDelta = path.join(cwd, `download_delta_${inst}_${delta.ts}.zip`);
                    try {
                        console.log(JSON.stringify({ type: 'PROGRESS', step: 'APPLYING_DELTA', value: 0, instance: inst, delta: delta.name }));
                        await provider.downloadFile(
                            delta.file.id,
                            tempDelta,
                            null, 
                            parseInt(delta.file.size, 10) || 0
                        );
                        applyDelta(tempDelta, targetPath, (pct) => {
                            console.log(JSON.stringify({ type: 'PROGRESS', step: 'APPLYING_DELTA', value: pct, instance: inst, delta: delta.name }));
                        });
                    } finally {
                        try { if (fs.existsSync(tempDelta)) fs.unlinkSync(tempDelta); } catch (_) {}
                    }
                }

                const lastDelta  = pendingDeltas.length > 0 ? pendingDeltas[pendingDeltas.length - 1] : null;
                syncState[inst]  = lastDelta ? new Date(lastDelta.ts).toISOString() : baseFile.modifiedTime;
                fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));

                const deltasApplied = pendingDeltas.length;
                console.log(JSON.stringify({
                    type    : 'SUCCESS',
                    instance: inst,
                    base    : baseChanged,
                    deltas  : deltasApplied,
                    message : baseChanged
                        ? `Base + ${deltasApplied} delta(s) appliqué(s).`
                        : `${deltasApplied} delta(s) appliqué(s).`
                }));

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