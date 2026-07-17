'use strict';
const { withRetry } = require('./retry');

/**
 * Récupère la liste de tous les fichiers du Cloud appartenant à Horizon
 * et supprime automatiquement les doublons (noms de fichiers identiques).
 *
 * @param {Object} provider L'instance du fournisseur Cloud
 * @param {Object} retryOpts Options de réessai
 * @param {string} logPrefix Préfixe pour les logs (ex: '[sync]', '[upload]')
 * @returns {Promise<Object>} Un dictionnaire nom_fichier -> metadata
 */
async function getCloudIndexAndCleanDuplicates(provider, retryOpts, logPrefix) {
    const cloudFiles = await withRetry(() => provider.listFiles('GensHorizon_'), { ...retryOpts, label: 'listFiles' });
    const cloudIndex = {};
    for (const f of cloudFiles) {
        if (!cloudIndex[f.name]) {
            cloudIndex[f.name] = f;
        } else {
            try {
                await withRetry(() => provider.deleteFile(f.id), { ...retryOpts, label: `deleteDuplicate(${f.name})` });
                process.stderr.write(`${logPrefix} Doublon supprimé du Cloud : ${f.name}\n`);
            } catch (e) {
                process.stderr.write(`${logPrefix} Échec suppression doublon ${f.name} : ${e.message}\n`);
            }
        }
    }
    return cloudIndex;
}

module.exports = {
    getCloudIndexAndCleanDuplicates
};
