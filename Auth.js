'use strict';

const fs     = require('fs');
const crypto = require('crypto');
const os     = require('os');

let username = "default";
try {
    username = os.userInfo().username;
} catch (e) {
    username = process.env.USER || process.env.LOGNAME || "unknown";
}
const machineID  = os.hostname() + '_' + username;
const SECRET_KEY = crypto.createHash('sha256').update(machineID).digest();

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

const TOKEN_FILE_MODE = 0o600;

function getSecureToken(filePath) {
    if (!fs.existsSync(filePath)) return null;
    const raw = fs.readFileSync(filePath, 'utf8').trim();
    if (raw.startsWith('{')) {
        const parsed = JSON.parse(raw);
        process.stderr.write(JSON.stringify({ type: 'INFO', message: 'Sécurisation du token en cours...' }) + '\n');
        fs.writeFileSync(filePath, _encrypt(JSON.stringify(parsed)), { encoding: 'utf8', mode: TOKEN_FILE_MODE });
        return parsed;
    }
    try {
        return JSON.parse(_decrypt(raw));
    } catch (e) {
        throw new Error('Impossible de déchiffrer le token. (Machine différente ?)');
    }
}

function encryptToken(filePath, tokenData) {
    fs.writeFileSync(filePath, _encrypt(JSON.stringify(tokenData)), { encoding: 'utf8', mode: TOKEN_FILE_MODE });
}

module.exports = { getSecureToken, encryptToken };
