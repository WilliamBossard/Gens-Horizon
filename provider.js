'use strict';

const fs   = require('fs');
const path = require('path');
const { getSecureToken }  = require('./Auth');
const { credentials }     = require('./config');

/**
 * 
 * 
 * @param {object} settings
 * @returns {string}
 */
function getProviderName(settings) {
    const cliArg = process.argv.find(a => a.startsWith('--provider='));
    if (cliArg) return cliArg.split('=')[1].trim();
    return (settings && settings.provider) || 'google';
}

/**
 * 
 * 
 * @param {string} providerName
 * @param {string} cwd
 * @returns {string}
 */
function getTokenPath(providerName, cwd) {
    const specific = path.join(cwd, `token_${providerName}.json`);
    if (fs.existsSync(specific)) return specific;

    if (providerName === 'google') {
        const legacy = path.join(cwd, 'token.json');
        if (fs.existsSync(legacy)) return legacy;
    }
    return specific; 
}

/**
 * 
 * @param {object} settings  
 * @param {string} [cwd]     
 * @returns {Promise<object|null>}  
 */
async function getProvider(settings, cwd = process.cwd()) {
    const name      = getProviderName(settings);
    const tokenPath = getTokenPath(name, cwd);

    if (!fs.existsSync(tokenPath)) return null;

    const tokenData = getSecureToken(tokenPath);
    if (!tokenData) return null;

    switch (name) {
        case 'google': {
            const { GoogleProvider } = require('./providers/google');
            return new GoogleProvider(tokenData, credentials.google);
        }
        case 'dropbox': {
            const { DropboxProvider } = require('./providers/dropbox');
            return new DropboxProvider(tokenData, credentials.dropbox);
        }
        case 'onedrive': {
            const { OneDriveProvider } = require('./providers/onedrive');
            return new OneDriveProvider(tokenData, credentials.onedrive);
        }
        default:
            throw new Error(`Fournisseur inconnu : "${name}". Valeurs acceptées : google, dropbox, onedrive`);
    }
}

module.exports = { getProvider, getProviderName, getTokenPath };