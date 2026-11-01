'use strict';
const { withRetry } = require('./retry');
const { withConcurrency } = require('./utils');

/**
 * Récupère la liste de tous les fichiers du Cloud appartenant à Horizon
 * et supprime automatiquement les doublons (noms de fichiers identiques).
 * Conserve toujours la version la plus récente en comparant les modifiedTime.
 *
 * @param {Object} provider L'instance du fournisseur Cloud
 * @param {Object} retryOpts Options de réessai
 * @param {string} logPrefix Préfixe pour les logs (ex: '[sync]', '[upload]')
 * @returns {Promise<Object>} Un dictionnaire nom_fichier -> metadata
 */
async function getCloudIndexAndCleanDuplicates(provider, retryOpts, logPrefix) {
    const cloudFiles = await withRetry(() => provider.listFiles('GensHorizon_'), { ...retryOpts, label: 'listFiles' });
    const cloudIndex = {};
    const deleteTasks = [];

    for (const f of cloudFiles) {
        const existing = cloudIndex[f.name];
        if (!existing) {
            cloudIndex[f.name] = f;
        } else if (existing.id && f.id === existing.id) {
            continue;
        } else {
            const fTime = new Date(f.modifiedTime).getTime() || 0;
            const existTime = new Date(existing.modifiedTime).getTime() || 0;
            const isFNewer = fTime > existTime || (fTime === existTime && (parseInt(f.size, 10) || 0) > (parseInt(existing.size, 10) || 0));
            const toKeep = isFNewer ? f : existing;
            const toDelete = isFNewer ? existing : f;
            cloudIndex[f.name] = toKeep;

            deleteTasks.push(async () => {
                try {
                    await withRetry(() => provider.deleteFile(toDelete.id), { ...retryOpts, label: `deleteDuplicate(${toDelete.name})` });
                    process.stderr.write(`${logPrefix} Doublon supprimé du Cloud : ${toDelete.name}\n`);
                } catch (e) {
                    process.stderr.write(`${logPrefix} Échec suppression doublon ${toDelete.name} : ${e.message}\n`);
                }
            });
        }
    }

    if (deleteTasks.length > 0) {
        await withConcurrency(4, deleteTasks);
    }
    
    return cloudIndex;
}

module.exports = {
    getCloudIndexAndCleanDuplicates
};
