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
    return process.pkg ? path.dirname(process.execPath) : process.cwd();
}
function getAppDataPath() {
    if (process.platform === 'win32') {
        const appdata = process.env.APPDATA;
        if (!appdata) {
            const profile = process.env.USERPROFILE || os.homedir();
            return path.join(profile, 'AppData', 'Roaming');
        }
        return appdata;
    } else if (process.platform === 'darwin') {
        return path.join(os.homedir(), 'Library', 'Application Support');
    } else {
        return path.join(os.homedir(), '.config');
    }
}
function getInstancesFolder() {
    const appData = getAppDataPath();
    return path.join(appData, 'GensLauncher', 'instances');
}
/**
 * DÉCISION : la clé instance côté Horizon = nom du dossier sous instances/
 * (safeDir). instance.json peut contenir un nom affiché différent (espaces, etc.).
 */
function scanInstances() {
    const instancesDir = getInstancesFolder();
    if (!fs.existsSync(instancesDir)) {
        fs.mkdirSync(instancesDir, { recursive: true });
        return [];
    }
    const items = fs.readdirSync(instancesDir);
    const instances = [];
    for (const item of items) {
        try {
            const fullPath = path.join(instancesDir, item);
            if (fs.statSync(fullPath).isDirectory()) {
                const jsonPath = path.join(fullPath, 'instance.json');
                if (fs.existsSync(jsonPath)) {
                    instances.push(item);
                }
            }
        } catch (_) {}
    }
    return instances;
}
function getProviderName(settings) {
    const cliArg = process.argv.find(a => a.startsWith('--provider='));
    if (cliArg) return cliArg.split('=')[1].trim();
    return (settings && settings.provider) || 'google';
}
module.exports = { getHorizonDataDir, getInstancesFolder, scanInstances, getProviderName };
