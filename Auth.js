'use strict';

/**
 * ==============================================================================
 * GENS HORIZON — CHIFFREMENT DES TOKENS OAuth
 * ==============================================================================
 * DÉCISION : salt.key et token_*.json vivent dans getHorizonDataDir() (bin),
 * aligné avec horizon_settings.json et horizon.lock — pas le dossier de l'exe seul.
 * ==============================================================================
 */

const fs     = require('fs');
const crypto = require('crypto');
const os     = require('os');
const path   = require('path');
const { getHorizonDataDir }          = require('./paths');
const { registerTemp, unregisterTemp } = require('./utils');

let username = 'default';
try {
    username = os.userInfo().username;
} catch (e) {
    username = process.env.USER || process.env.LOGNAME || 'unknown';
}
const machineID = os.hostname() + '_' + username;
const BASE_DIR = getHorizonDataDir();

const SALT_FILE       = path.join(BASE_DIR, 'salt.key');
const TOKEN_FILE_MODE = 0o600;

let salt;
if (fs.existsSync(SALT_FILE)) {
    salt = fs.readFileSync(SALT_FILE);
} else {
    salt = crypto.randomBytes(16);
    try {
        fs.writeFileSync(SALT_FILE, salt, { mode: TOKEN_FILE_MODE });
    } catch (e) {
        throw new Error(`[Auth] ERREUR CRITIQUE : Impossible d'écrire le fichier de sécurité (salt.key). Vérifiez les permissions. Détail : ${e.message}`);
    }
}

const SECRET_KEY = crypto.pbkdf2Sync(machineID, salt, 100000, 32, 'sha256');

function _encrypt(text) {
    const iv     = crypto.randomBytes(16);
    const cipher = crypto.createCipheriv('aes-256-cbc', SECRET_KEY, iv);
    let encrypted = cipher.update(text, 'utf8', 'hex');
    encrypted    += cipher.final('hex');
    return iv.toString('hex') + ':' + encrypted;
}

function _decrypt(text) {
    try {
        const parts    = text.split(':');
        const iv       = Buffer.from(parts.shift(), 'hex');
        const decipher = crypto.createDecipheriv('aes-256-cbc', SECRET_KEY, iv);
        let decrypted  = decipher.update(parts.join(':'), 'hex', 'utf8');
        decrypted     += decipher.final('utf8');
        return decrypted;
    } catch (e) {
        throw new Error('Impossible de déchiffrer le token. (Machine différente ou fichier corrompu ?)');
    }
}

function getSecureToken(filePath) {
    if (!fs.existsSync(filePath)) return null;
    const raw = fs.readFileSync(filePath, 'utf8').trim();
    if (raw.startsWith('{')) {
        const parsed = JSON.parse(raw);
        process.stderr.write(JSON.stringify({ type: 'INFO', message: 'Sécurisation du token en cours...' }) + '\n');
        encryptToken(filePath, parsed);
        return parsed;
    }
    try {
        return JSON.parse(_decrypt(raw));
    } catch (e) {
        throw new Error('Impossible de déchiffrer le token. (Machine différente ?)');
    }
}

function encryptToken(filePath, tokenData) {
    const tmp = filePath + '.tmp';
    registerTemp(tmp);

    fs.writeFileSync(tmp, _encrypt(JSON.stringify(tokenData)), {
        encoding: 'utf8',
        mode: TOKEN_FILE_MODE
    });

    fs.renameSync(tmp, filePath);
    unregisterTemp(tmp);
}

module.exports = { getSecureToken, encryptToken };