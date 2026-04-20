const fs = require('fs');
const crypto = require('crypto');
const os = require('os');

const machineID = os.hostname() + "_" + os.userInfo().username;
const SECRET_KEY = crypto.createHash('sha256').update(machineID).digest();

/**
 * 
 * 
 * 
 *
 * @param {string} filePath  
 * @returns {object|null}    
 * @throws {Error}           
 */
function getSecureToken(filePath) {
    if (!fs.existsSync(filePath)) return null;

    const raw = fs.readFileSync(filePath, 'utf8').trim();

    if (raw.startsWith('{')) {
        const parsed = JSON.parse(raw);
        console.log(JSON.stringify({ type: "INFO", message: "Sécurisation du token en cours..." }));

        const iv = crypto.randomBytes(16);
        const cipher = crypto.createCipheriv('aes-256-cbc', SECRET_KEY, iv);
        let encrypted = cipher.update(JSON.stringify(parsed), 'utf8', 'hex');
        encrypted += cipher.final('hex');

        fs.writeFileSync(filePath, iv.toString('hex') + ':' + encrypted, 'utf8');
        return parsed;
    }

    try {
        const parts = raw.split(':');
        const iv = Buffer.from(parts.shift(), 'hex');
        const decipher = crypto.createDecipheriv('aes-256-cbc', SECRET_KEY, iv);
        let decrypted = decipher.update(parts.join(':'), 'hex', 'utf8');
        decrypted += decipher.final('utf8');
        return JSON.parse(decrypted);
    } catch (e) {
        throw new Error("Impossible de déchiffrer le token sécurisé. (Machine différente ?)");
    }
}

module.exports = { getSecureToken };