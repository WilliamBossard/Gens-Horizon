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
    const instances = [];
    for (const item of items) {
        try {
            const fullPath = path.join(instancesDir, item);
            if (fs.statSync(fullPath).isDirectory()) {
                const jsonPath = path.join(fullPath, 'instance.json');
                if (fs.existsSync(jsonPath)) {
                    const data = JSON.parse(fs.readFileSync(jsonPath, 'utf8'));
                    if (data.name) {
                        instances.push(data.name);
                        continue; 
                    }
                }
                instances.push(item);
            }
        } catch (_) {}
    }
    return instances;
}

module.exports = { getInstancesFolder, scanInstances };
