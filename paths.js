const os   = require('os');
const path = require('path');
const fs   = require('fs');

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

function scanInstances() {
    const instancesDir = getInstancesFolder();

    if (!fs.existsSync(instancesDir)) {
        fs.mkdirSync(instancesDir, { recursive: true });
        return [];
    }

    const items = fs.readdirSync(instancesDir);
    const instances = items.filter(item => {
        try {
            const fullPath = path.join(instancesDir, item);
            return fs.statSync(fullPath).isDirectory();
        } catch (_) {
            return false;
        }
    });

    return instances;
}

module.exports = { getInstancesFolder, scanInstances };
