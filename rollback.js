'use strict';
const fs                          = require('fs');
const path                        = require('path');
const { getInstancesFolder, getHorizonDataDir } = require('./paths');
const { getCanonicalName, setupProcessHandlers, writeJsonAtomicAsync, existsSafe, parseTargetInstance } = require('./utils');
const { acquireLock, releaseLock } = require('./lock');
const { generateManifest } = require('./scanner');
setupProcessHandlers();

async function rollback() {
    const args           = process.argv.slice(2);
    const targetInstance = parseTargetInstance(args);

    if (!targetInstance) {
        const instDir = getInstancesFolder();
        if (!(await existsSafe(instDir))) {
            console.log(JSON.stringify({ type: 'INFO', message: "Aucun dossier d'instances trouvé." }));
            return;
        }
        const rollbacks = (await fs.promises.readdir(instDir))
            .filter(n => n.includes('_rollback_'))
            .map(n => {
                const lastIdx = n.lastIndexOf('_rollback_');
                const tsStr = n.substring(lastIdx + '_rollback_'.length);
                const ts    = parseInt(tsStr, 10);
                return {
                    folder   : n,
                    instance : n.substring(0, lastIdx),
                    timestamp: isNaN(ts) ? null : new Date(ts).toISOString(),
                };
            });
        console.log(JSON.stringify({ type: 'ROLLBACK_LIST', data: rollbacks }));
        return;
    }
    const safeInst = getCanonicalName(targetInstance);
    const instDir    = getInstancesFolder();
    const rawPath = path.join(instDir, targetInstance);
    const safePath = path.join(instDir, safeInst);
    const targetPath = (await existsSafe(rawPath)) ? rawPath : safePath;

    let rollbackFolder = null;
    let rollbackTime   = 0;
    if (await existsSafe(instDir)) {
        for (const entry of await fs.promises.readdir(instDir)) {
            if (entry.startsWith(`${safeInst}_rollback_`) || entry.startsWith(`${targetInstance}_rollback_`)) {
                const lastIdx = entry.lastIndexOf('_rollback_');
                const ts = parseInt(entry.substring(lastIdx + '_rollback_'.length), 10);
                if (!isNaN(ts) && ts > rollbackTime) {
                    rollbackTime   = ts;
                    rollbackFolder = path.join(instDir, entry);
                }
            }
        }
    }
    if (!rollbackFolder) {
        console.log(JSON.stringify({ type: 'ERROR', instance: targetInstance, message: 'Aucune sauvegarde rollback disponible pour cette instance.' }));
        return;
    }
    if (!(await acquireLock())) {
        console.log(JSON.stringify({ type: 'ERROR', instance: targetInstance, message: 'ERR_ALREADY_RUNNING' }));
        return;
    }
    try {
        if (await existsSafe(targetPath)) {
            await fs.promises.rm(targetPath, { recursive: true, force: true });
        }
        // Sous Windows, attendre un léger délai si l'OS libère encore le descripteur de suppression
        let renamed = false;
        for (let i = 0; i < 5; i++) {
            try {
                await fs.promises.rename(rollbackFolder, targetPath);
                renamed = true;
                break;
            } catch (rErr) {
                if (i === 4) throw rErr;
                await new Promise(r => setTimeout(r, 50 * (i + 1)));
            }
        }

        const syncInfoPath = path.join(getHorizonDataDir(), 'last_sync.json');
        let syncState = {};
        if (await existsSafe(syncInfoPath)) {
            try { syncState = JSON.parse(await fs.promises.readFile(syncInfoPath, 'utf8')); } catch(err){ if (err.code !== 'ENOENT') process.stderr.write(`[rollback] Erreur lecture sync state: ${err.message}\n`); }
        }
        delete syncState[safeInst];
        await writeJsonAtomicAsync(syncInfoPath, syncState);

        try {
            const newManifest = await generateManifest(targetPath);
            await writeJsonAtomicAsync(path.join(getHorizonDataDir(), `manifest_${safeInst}.json`), newManifest);
        } catch (mErr) {
            process.stderr.write(`[rollback] Erreur régénération manifest: ${mErr.message}\n`);
        }

        console.log(JSON.stringify({
            type    : 'SUCCESS',
            instance: targetInstance,
            message : `Instance restaurée depuis la sauvegarde du ${new Date(rollbackTime).toLocaleString()}.`,
        }));
    } catch (e) {
        console.log(JSON.stringify({ type: 'ERROR', instance: targetInstance, message: e.message }));
    } finally {
        releaseLock();
    }
}
rollback();
