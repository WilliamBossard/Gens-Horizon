'use strict';
const fs = require('fs');
const path = require('path');
const { getHorizonDataDir } = require('./paths');
const { withRetry } = require('./retry');
const { getCanonicalName, readJsonSafe, writeJsonAtomicAsync } = require('./utils');

async function deleteInstance(targetInstance, cloudIndex, provider, retryOpts) {
    const dataDir = getHorizonDataDir();
    const syncInfoPath = path.join(dataDir, 'last_sync.json');
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
    await fs.promises.rm(manifestPath, { force: true });
    
    try {
        await fs.promises.access(syncInfoPath);
        const syncState = await readJsonSafe(syncInfoPath);
        if (syncState[safeTarget]) {
            delete syncState[safeTarget];
            await writeJsonAtomicAsync(syncInfoPath, syncState);
        }
    } catch (e) {
        // Ignorer si le fichier n'existe pas
    }
    
    const metaPath = path.join(dataDir, `meta_${safeTarget}.json`);
    await fs.promises.rm(metaPath, { force: true });
    
    console.log(JSON.stringify({ type: 'SUCCESS', instance: targetInstance, message: 'Supprimé du cloud.' }));
}

module.exports = { deleteInstance };
