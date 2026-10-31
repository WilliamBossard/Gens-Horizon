/**
 * ==============================================================================
 * GENS HORIZON — CHEMINS & SCAN INSTANCES
 * ==============================================================================
 * DÉCISION ARCHITECTURALE :
 * - getHorizonDataDir() = même dossier que le cwd du spawn Gens-Launcher
 *   (%AppData%/GensLauncher/bin). En pkg : répertoire de Horizon.exe, pas le cwd shell.
 * - scanInstances() retourne toujours le nom de DOSSIER (clé disque / Horizon), pas data.name.
 * ==============================================================================
 */
const os   = require('os');
const path = require('path');
const fs   = require('fs');

function getHorizonDataDir() {
    if (process.env.HORIZON_DATA_DIR) return process.env.HORIZON_DATA_DIR;
    return process.pkg ? path.dirname(process.execPath) : process.cwd();
}

function getAppDataPath() {
    if (process.env.APPDATA) return process.env.APPDATA;
    if (process.platform === 'win32') {
        const profile = process.env.USERPROFILE || os.homedir();
        return path.join(profile, 'AppData', 'Roaming');
    } else if (process.platform === 'darwin') {
        return path.join(process.env.HOME || os.homedir(), 'Library', 'Application Support');
    } else {
        return process.env.XDG_CONFIG_HOME || path.join(process.env.HOME || os.homedir(), '.config');
    }
}

function getInstancesFolder() {
    if (process.env.GENS_INSTANCES_DIR) return process.env.GENS_INSTANCES_DIR;
    const appData = getAppDataPath();
    return path.join(appData, 'GensLauncher', 'instances');
}

/**
 * scanInstances() retourne toujours le nom de DOSSIER (clé disque / Horizon), pas data.name.
 */
async function scanInstances() {
    const instancesDir = getInstancesFolder();
    try {
        await fs.promises.mkdir(instancesDir, { recursive: true });
    } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[paths.js] Erreur silencieuse interceptée:', _.message || _); }
    let items;
    try {
        items = await fs.promises.readdir(instancesDir, { withFileTypes: true });
    } catch (_) {
        return [];
    }
    const instances = [];
    for (const item of items) {
        if (!item.isDirectory()) continue;
        if (item.name.includes('_rollback_')) continue;
        try {
            await fs.promises.access(path.join(instancesDir, item.name, 'instance.json'));
            instances.push(item.name);
        } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[paths.js] Erreur silencieuse interceptée:', _.message || _); }
    }
    return instances;
}

function getProviderName(settings) {
    const VALID = new Set(['google', 'dropbox', 'onedrive']);
    const cliArg = process.argv.find(a => a.startsWith('--provider='));
    if (cliArg) {
        const raw = cliArg.split('=')[1].trim().toLowerCase();
        if (VALID.has(raw)) return raw;
    }
    const provIdx = process.argv.indexOf('--provider');
    if (provIdx !== -1 && process.argv[provIdx + 1]) {
        const raw = process.argv[provIdx + 1].trim().toLowerCase();
        if (VALID.has(raw)) return raw;
    }
    const positional = process.argv.slice(2).find(a => VALID.has(a.trim().toLowerCase()));
    if (positional) return positional.trim().toLowerCase();

    return (settings && settings.provider) || 'google';
}

module.exports = { getHorizonDataDir, getInstancesFolder, scanInstances, getProviderName, getAppDataPath };
