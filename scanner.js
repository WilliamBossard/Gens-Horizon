'use strict';

const fs     = require('fs');
const fsP    = fs.promises;
const path   = require('path');
const crypto = require('crypto');
const IGNORED = new Set([
    'versions', 'libraries', 'assets', 'logs',
    'crash-reports', 'cache', 'webcache', 'temp',
    'screenshots', 'backups', '.git', 'node_modules',
]);

/**
 * Calcule le Hash SHA-1 d'un fichier sans saturer la RAM (Lecture par flux)
 */
function hashFile(filePath) {
    return new Promise((resolve, reject) => {
        const hash = crypto.createHash('sha1');
        const stream = fs.createReadStream(filePath);
        stream.on('data', chunk => hash.update(chunk));
        stream.on('end', () => resolve(hash.digest('hex')));
        stream.on('error', reject);
    });
}

/**
 * * @param {string} targetPath  
 * @param {string} basePath    
 * @returns {Promise<Object.<string,string>>}
 */
async function generateManifest(targetPath, basePath = targetPath) {
    const manifest = {};
    try {
        const items = await fsP.readdir(targetPath, { withFileTypes: true });
        for (const item of items) {
            if (IGNORED.has(item.name) || item.name.startsWith('.')) continue;

            const fullPath     = path.join(targetPath, item.name);
            const relativePath = path.relative(basePath, fullPath).replace(/\\/g, '/');

            if (item.isDirectory()) {
                Object.assign(manifest, await generateManifest(fullPath, basePath));
            } else {
                manifest[relativePath] = await hashFile(fullPath);
            }
        }
    } catch (_) {}
    return manifest;
}

/**
 * *
 * @param {Object.<string,string>} oldM  
 * @param {Object.<string,string>} newM  
 * @returns {{
 * hasChanges : boolean,
 * added      : string[],   
 * modified   : string[],   
 * deleted    : string[]    
 * }}
 */
function compareManifests(oldM, newM) {
    const added    = Object.keys(newM).filter(f => !oldM[f]);
    const modified = Object.keys(newM).filter(f => oldM[f] && oldM[f] !== newM[f]);
    const deleted  = Object.keys(oldM).filter(f => !newM[f]);

    return {
        hasChanges: added.length > 0 || modified.length > 0 || deleted.length > 0,
        added,
        modified,
        deleted
    };
}

module.exports = { generateManifest, compareManifests };