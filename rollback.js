'use strict';

const fs   = require('fs');
const path = require('path');

const { getInstancesFolder }      = require('./paths');
const { sanitizeInstanceName, getFolderFromName }    = require('./utils');

function rollback() {
    const args           = process.argv.slice(2);
    const COMMANDS       = new Set(['rollback']);
    const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

    if (!targetInstance) {
        const instDir = getInstancesFolder();
        if (!fs.existsSync(instDir)) {
            console.log(JSON.stringify({ type: 'INFO', message: "Aucun dossier d'instances trouvé." }));
            return;
        }
        const rollbacks = fs.readdirSync(instDir)
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

    const safeInst   = sanitizeInstanceName(targetInstance);
    const instDir    = getInstancesFolder();
    const targetPath = path.join(instDir, getFolderFromName(targetInstance));

    let rollbackFolder = null;
    let rollbackTime   = 0;
    if (fs.existsSync(instDir)) {
        for (const entry of fs.readdirSync(instDir)) {
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

    try {
        if (fs.existsSync(targetPath)) {
            fs.rmSync(targetPath, { recursive: true, force: true });
        }

        fs.renameSync(rollbackFolder, targetPath);

        console.log(JSON.stringify({
            type    : 'SUCCESS',
            instance: targetInstance,
            message : `Instance restaurée depuis la sauvegarde du ${new Date(rollbackTime).toLocaleString()}.`,
        }));
    } catch (e) {
        console.log(JSON.stringify({ type: 'ERROR', instance: targetInstance, message: e.message }));
    }
}

rollback();