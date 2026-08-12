'use strict';
/**
 * ==============================================================================
 * GENS HORIZON — CHIFFREMENT DES TOKENS OAuth
 * ==============================================================================
 * DÉCISION : salt.key et token_*.json vivent dans getHorizonDataDir() (bin),
 * aligné avec horizon_settings.json et horizon.lock — pas le dossier de l'exe seul.
 * ==============================================================================
 */
const fs = require('fs');
const crypto = require('crypto');
const os = require('os');
const path = require('path');
const { getHorizonDataDir } = require('./paths');
const { registerTemp, unregisterTemp } = require('./utils');
const BASE_DIR = getHorizonDataDir();
const MACHINE_ID_FILE = path.join(BASE_DIR, '.machine_id');
const SALT_FILE = path.join(BASE_DIR, 'salt.key');
const TOKEN_FILE_MODE = 0o600;

let machineID = null;
let salt = null;

async function initAuthData() {
    if (machineID && salt) return;
    
    if (await existsSafe(MACHINE_ID_FILE)) {
        machineID = (await fs.promises.readFile(MACHINE_ID_FILE, 'utf8')).trim();
    } else {
        machineID = crypto.randomBytes(32).toString('hex');
        try {
            await fs.promises.writeFile(MACHINE_ID_FILE, machineID, { mode: 0o600 });
        } catch (e) {
            process.stderr.write(`[Auth] AVERTISSEMENT : Création de .machine_id échouée (${e.message}). Utilisation du fallback matériel.\n`);
            const nets = os.networkInterfaces();
            let mac = '00:00:00:00:00:00';
            for (const name of Object.keys(nets)) {
                for (const net of nets[name]) {
                    if (!net.internal && net.mac !== '00:00:00:00:00:00') {
                        mac = net.mac;
                        break;
                    }
                }
                if (mac !== '00:00:00:00:00:00') break;
            }
            machineID = crypto.createHash('sha256').update(os.hostname() + mac + (os.userInfo().username || '')).digest('hex');
        }
    }

    if (await existsSafe(SALT_FILE)) {
        salt = await fs.promises.readFile(SALT_FILE);
    } else {
        salt = crypto.randomBytes(16);
        try {
            await fs.promises.writeFile(SALT_FILE, salt, { mode: TOKEN_FILE_MODE });
        } catch (e) {
            throw new Error(`[Auth] ERREUR CRITIQUE : Impossible d'écrire le fichier de sécurité (salt.key). Vérifiez les permissions. Détail : ${e.message}`);
        }
    }
}

let SECRET_KEY_PROMISE = null;
function getSecretKey() {
    if (SECRET_KEY_PROMISE) return SECRET_KEY_PROMISE;
    SECRET_KEY_PROMISE = (async () => {
        await initAuthData();
        return new Promise((resolve, reject) => {
            crypto.pbkdf2(machineID, salt, 600000, 32, 'sha256', (err, derivedKey) => {
                if (err) reject(err);
                else resolve(derivedKey);
            });
        });
    })();
    return SECRET_KEY_PROMISE;
}
async function _encrypt(text) {
    const key = await getSecretKey();
    const iv = crypto.randomBytes(12);
    const cipher = crypto.createCipheriv('aes-256-gcm', key, iv);
    let encrypted = cipher.update(text, 'utf8', 'hex');
    encrypted += cipher.final('hex');
    const authTag = cipher.getAuthTag().toString('hex');
    return iv.toString('hex') + ':' + authTag + ':' + encrypted;
}
async function _decrypt(text) {
    try {
        const key = await getSecretKey();
        const parts = text.split(':');

        if (parts.length === 3) {
            const iv = Buffer.from(parts[0], 'hex');
            const authTag = Buffer.from(parts[1], 'hex');
            const encrypted = parts[2];
            const decipher = crypto.createDecipheriv('aes-256-gcm', key, iv);
            decipher.setAuthTag(authTag);
            let decrypted = decipher.update(encrypted, 'hex', 'utf8');
            decrypted += decipher.final('utf8');
            return { decrypted, needsMigration: false };
        } else {
            const iv = Buffer.from(parts[0], 'hex');
            const decipher = crypto.createDecipheriv('aes-256-cbc', key, iv);
            let decrypted = decipher.update(parts.slice(1).join(':'), 'hex', 'utf8');
            decrypted += decipher.final('utf8');
            return { decrypted, needsMigration: true };
        }
    } catch (e) {
        throw new Error('Impossible de déchiffrer le token. (Machine différente ou fichier corrompu ?)');
    }
}
async function getSecureToken(filePath) {
    if (!(await existsSafe(filePath))) return null;
    const raw = (await fs.promises.readFile(filePath, 'utf8')).trim();
    if (raw.startsWith('{')) {
        const parsed = JSON.parse(raw);
        process.stderr.write(JSON.stringify({ type: 'INFO', message: 'Sécurisation du token en cours...' }) + '\n');
        await encryptToken(filePath, parsed);
        return parsed;
    }
    try {
        const { decrypted, needsMigration } = await _decrypt(raw);
        const parsed = JSON.parse(decrypted);
        if (needsMigration) {
            process.stderr.write(JSON.stringify({ type: 'INFO', message: 'Migration du token vers AES-GCM en cours...' }) + '\n');
            await encryptToken(filePath, parsed);
        }
        return parsed;
    } catch (e) {
        throw new Error('Impossible de déchiffrer le token. (Machine différente ?)');
    }
}
async function encryptToken(filePath, tokenData) {
    const tmp = filePath + '.tmp';
    registerTemp(tmp);
    const encrypted = await _encrypt(JSON.stringify(tokenData));
    await fs.promises.writeFile(tmp, encrypted, {
        encoding: 'utf8',
        mode: TOKEN_FILE_MODE
    });
    await fs.promises.rename(tmp, filePath);
    unregisterTemp(tmp);
}
module.exports = {
    getSecureToken,
    encryptToken,
    ...(process.env.NODE_ENV === 'test' ? { _encrypt, _decrypt } : {})
};
