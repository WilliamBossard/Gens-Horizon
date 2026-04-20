const fs = require('fs');
const path = require('path');
const SETTINGS_PATH = path.join(process.cwd(), 'horizon_settings.json');

if (!fs.existsSync(SETTINGS_PATH)) {
    const defaultSettings = {
        systemEnabled: true, 
        syncMode: "SMART",
        autoSync: true,
        autoUpload: true
    };
    fs.writeFileSync(SETTINGS_PATH, JSON.stringify(defaultSettings, null, 2));
    console.log(JSON.stringify({ type: "INFO", message: "Réglages Horizon initialisés." }));
}

const args = process.argv.slice(2);

if (args.includes('--check')) {
    require('./check.js');
}
else if (args.includes('--sync')) {
    require('./sync.js');
}
else if (args.includes('--upload')) {
    require('./upload.js');
}
else if (args.includes('--login')) {
    require('./login.js');
}
else {
    console.error(JSON.stringify({ status: "ERROR", message: "Commande manquante." }));
}