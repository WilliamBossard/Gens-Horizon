const os = require('os');
const path = require('path');
const fs = require('fs');

function getAppDataPath() {
    if (process.platform === 'win32') {
        return process.env.APPDATA; 
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
        const fullPath = path.join(instancesDir, item);
        return fs.statSync(fullPath).isDirectory();
    });

    return instances;
}

module.exports = { getInstancesFolder, scanInstances };