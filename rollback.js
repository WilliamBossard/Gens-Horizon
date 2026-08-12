'use strict';
const fs                          = require('fs');
const path                        = require('path');
const crypto                      = require('crypto');
const { getInstancesFolder, getHorizonDataDir } = require('./paths');
const { getCanonicalName, setupProcessHandlers, writeJsonAtomic } = require('./utils');
const { acquireLock, releaseLock } = require('./lock');
setupProcessHandlers();
async function rollback() {
    const args           = process.argv.slice(2);
    const COMMANDS       = new Set(['rollback']);
    const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));
    if (!targetInstance) {
        const instDir = getInstancesFolder();
        if (!(await existsSafe(instDir))) {
            console.log(JSON.stringify({ type: 'INFO', message: "Aucun dossier d'instances trouvé." }));
            return;
        }
        const rollbacks = (await fs.promises.readdir(instDir))
            .filter(n => n.includes('_rollback_'))
            .map(n => {
                const tsStr = n.split('_rollback_').pop();
                const ts    = parseInt(tsStr, 10);
                return {
                    folder   : n,
                    instance : n.split('_rollback_')[0],
                    timestamp: isNaN(ts) ? null : new Date(ts).toISOString(),
                };
            });
        console.log(JSON.stringify({ type: 'ROLLBACK_LIST', data: rollbacks }));
        return;
    }
    const safeInst = getCanonicalName(targetInstance);
    const instDir    = getInstancesFolder();
    const targetPath = path.join(instDir, safeInst);
    let rollbackFolder = null;
    let rollbackTime   = 0;
    if (await existsSafe(instDir)) {
        for (const entry of await fs.promises.readdir(instDir)) {
            if (entry.startsWith(`${safeInst}_rollback_`)) {
                const ts = parseInt(entry.split('_rollback_').pop(), 10);
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
        await fs.promises.rename(rollbackFolder, targetPath);

        const syncInfoPath = path.join(getHorizonDataDir(), 'last_sync.json');
        let syncState = {};
        if (await existsSafe(syncInfoPath)) {
            try { syncState = JSON.parse(await fs.promises.readFile(syncInfoPath, 'utf8')); } catch(err){ if (err.code !== 'ENOENT') process.stderr.write(`[rollback] Erreur lecture sync state: ${err.message}\n`); }
        }
        syncState[safeInst] = new Date(rollbackTime).toISOString();
        await writeJsonAtomic(syncInfoPath, syncState);

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
