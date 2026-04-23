/**
 * upload.js
 * Upload des instances Minecraft vers le Cloud.
 *
 * ── Modes ────────────────────────────────────────────────────────────────────
 *  FULL  : rezippe et remplace l'intégralité de l'instance sur le Cloud.
 *          Équivalent de l'ancien comportement.
 *
 *  SMART : mode incrémental (défaut).
 *          - Premier upload → crée un backup complet (base ZIP).
 *          - Uploads suivants → crée uniquement un delta ZIP contenant
 *            les fichiers ajoutés/modifiés + un __delta__.json listant
 *            les fichiers à supprimer. Les deltas s'accumulent sur le Cloud.
 *          - --force → supprime tous les deltas et recrée un base complet.
 *
 * ── Nommage des fichiers Cloud ────────────────────────────────────────────────
 *  Base    : GensHorizon_Backup_{instance}.zip
 *  Delta   : GensHorizon_Delta_{instance}_{timestamp}.zip
 *  Manifest: GensHorizon_Manifest_{instance}.json   (manifest cloud courant)
 *
 * ── Arguments CLI ─────────────────────────────────────────────────────────────
 *  --upload [instanceName]  Uploade une instance spécifique ou toutes
 *  --force                  Force un base complet (ignore le manifest local)
 *  --provider=xxx           google | dropbox | onedrive (sinon : settings.provider)
 */

'use strict';

const fs      = require('fs');
const archiver = require('archiver');
const AdmZip  = require('adm-zip');
const path    = require('path');
const dns     = require('dns').promises;

const { getInstancesFolder, scanInstances } = require('./paths');
const { generateManifest, compareManifests } = require('./scanner');
const { getProvider }                        = require('./provider');

function getFolderSizeSync(dir) {
    let total = 0;
    try {
        for (const f of fs.readdirSync(dir)) {
            const p    = path.join(dir, f);
            const stat = fs.statSync(p);
            total += stat.isDirectory() ? getFolderSizeSync(p) : stat.size;
        }
    } catch (_) {}
    return total;
}

/**
 * Crée un ZIP complet d'un dossier avec rapport de progression.
 */
function createFullZip(folder, tempZip, inst) {
    const realTotal = getFolderSizeSync(folder);
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
}

/**
 * @param {string}   folder      
 * @param {string[]} changed     
 * @param {string[]} deleted     
 * @param {string}   tempZip     
 * @param {string}   inst        
 */
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

async function upload() {
    try {
        try {
            await dns.lookup('google.com');
        } catch (dnsErr) {
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
                    fs.unlinkSync(tempZip);

                    const manifestExisting = cloudIndex[manifestName] ? cloudIndex[manifestName].id : null;
                    await provider.uploadJSON(manifestName, currentManifest, manifestExisting);
                    const syncState = fs.existsSync(syncInfoPath) ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8')) : {};
                    syncState[inst] = result?.modifiedTime || new Date().toISOString();
                    fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));
                    fs.writeFileSync(manifestPath, JSON.stringify(currentManifest, null, 2));
                    console.log(JSON.stringify({ type: 'SUCCESS', instance: inst, mode: 'FULL' }));
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
                fs.unlinkSync(tempDelta);
                const manifestExisting = cloudIndex[manifestName] ? cloudIndex[manifestName].id : null;
                await provider.uploadJSON(manifestName, currentManifest, manifestExisting);
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