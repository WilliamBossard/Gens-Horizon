const fs = require('fs').promises;
const path = require('path');
const crypto = require('crypto');
const IGNORED = new Set([
    'versions',
    'libraries',
    'assets',
    'logs',
    'crash-reports',
    'cache',
    'webcache',
    'temp',
    'screenshots',
    'backups',
    '.git',
    'node_modules',
]);

async function generateManifest(targetPath, basePath = targetPath) {
    const manifest = {};
    try {
        const items = await fs.readdir(targetPath, { withFileTypes: true });
        for (const item of items) {
            if (IGNORED.has(item.name) || item.name.startsWith('.')) continue;

            const fullPath = path.join(targetPath, item.name);
            const relativePath = path.relative(basePath, fullPath).replace(/\\/g, '/');

            if (item.isDirectory()) {
                Object.assign(manifest, await generateManifest(fullPath, basePath));
            } else {
                const buffer = await fs.readFile(fullPath);
                manifest[relativePath] = crypto.createHash('sha1').update(buffer).digest('hex');
            }
        }
    } catch (e) {
    }
    return manifest;
}

function compareManifests(oldM, newM) {
    const added    = Object.keys(newM).filter(f => !oldM[f]);
    const modified = Object.keys(newM).filter(f => oldM[f] && oldM[f] !== newM[f]);
    const deleted  = Object.keys(oldM).filter(f => !newM[f]);
    return { hasChanges: added.length > 0 || modified.length > 0 || deleted.length > 0 };
}

module.exports = { generateManifest, compareManifests };