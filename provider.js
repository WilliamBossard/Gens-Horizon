'use strict';

const fs   = require('fs');
const path = require('path');
const { getSecureToken }  = require('./Auth');
const { credentials }     = require('./config');
const BASE_DIR = process.pkg
    ? path.dirname(process.execPath)
    : path.dirname(process.argv[1]);

function getProviderName(settings) {
    const cliArg = process.argv.find(a => a.startsWith('--provider='));
    if (cliArg) return cliArg.split('=')[1].trim();
    return (settings && settings.provider) || 'google';
}

function getTokenPath(providerName, _cwd) {
    const specific = path.join(BASE_DIR, `token_${providerName}.json`);
    if (fs.existsSync(specific)) return specific;

    if (providerName === 'google') {
        const legacy = path.join(BASE_DIR, 'token.json');
        if (fs.existsSync(legacy)) return legacy;
    }
    return specific;
}

async function getProvider(settings, _cwd) {
    const name      = getProviderName(settings);
    const tokenPath = getTokenPath(name);

    if (!fs.existsSync(tokenPath)) return null;

    let tokenData;
    try {
        tokenData = getSecureToken(tokenPath);
    } catch (e) {
        process.stderr.write(`[provider] Token illisible pour "${name}" : ${e.message}\n`);
        return null;
    }

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