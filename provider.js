'use strict';
/**
 * ==============================================================================
 * GENS HORIZON — FACTORY FOURNISSEUR CLOUD
 * ==============================================================================
 * DÉCISION : tokens dans getHorizonDataDir() via paths.js (pas BASE_DIR local).
 * getProviderName vit dans paths.js pour un seul point de vérité.
 * ==============================================================================
 */
const fs   = require('fs');
const path = require('path');
const { getSecureToken }  = require('./Auth');
const { getConfig }       = require('./config');
const { getHorizonDataDir, getProviderName } = require('./paths');
function getTokenPath(providerName) {
    const base = getHorizonDataDir();
    const specific = path.join(base, `token_${providerName}.json`);
    if (fs.existsSync(specific)) return specific;
    if (providerName === 'google') {
        const legacy = path.join(base, 'token.json');
        if (fs.existsSync(legacy)) return legacy;
    }
    return specific;
}
async function getProvider(settings) {
    const name      = getProviderName(settings);
    const tokenPath = getTokenPath(name);
    if (!fs.existsSync(tokenPath)) return null;
    let tokenData;
    try {
        tokenData = await getSecureToken(tokenPath);
    } catch (e) {
        process.stderr.write(`[provider] Token illisible pour "${name}" : ${e.message}\n`);
        try { fs.unlinkSync(tokenPath); } catch (_) {}
        return null;
    }
    if (!tokenData) return null;
    switch (name) {
        case 'google': {
            const { GoogleProvider } = require('./providers/google');
            return new GoogleProvider(tokenData, getConfig('google'), tokenPath);
        }
        case 'dropbox': {
            const { DropboxProvider } = require('./providers/dropbox');
            return new DropboxProvider(tokenData, getConfig('dropbox'), tokenPath);
        }
        case 'onedrive': {
            const { OneDriveProvider } = require('./providers/onedrive');
            return new OneDriveProvider(tokenData, getConfig('onedrive'), tokenPath);
        }
        default:
            throw new Error(`Fournisseur inconnu : "${name}". Valeurs acceptées : google, dropbox, onedrive`);
    }
}
module.exports = { getProvider, getTokenPath };
