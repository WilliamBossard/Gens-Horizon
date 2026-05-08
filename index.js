'use strict';

const fs = require('fs');
const path = require('path');

const SETTINGS_PATH = path.join(process.cwd(), 'horizon_settings.json');

if (!fs.existsSync(SETTINGS_PATH)) {
    const defaultSettings = {
        systemEnabled    : true,
        syncMode         : 'SMART',
        autoSync         : true,
        autoUpload       : true,
        provider         : 'google',
        maxRetries       : 3,
        retryBaseDelay   : 1500,
        deltaCleanupThreshold: 10,
    };
    fs.writeFileSync(SETTINGS_PATH, JSON.stringify(defaultSettings, null, 2));
}

const args = process.argv.slice(2);

if (args.includes('--login')) {
    require('./login.js');
} else if (args.includes('--check')) {
    require('./check.js');
} else if (args.includes('--sync')) {
    require('./sync.js');
} else if (args.includes('--upload')) {
    require('./upload.js');
} else if (args.includes('--quota')) {
    require('./quota.js');
} else if (args.includes('--rollback')) {
    require('./rollback.js');
} else {
    console.log(JSON.stringify({
        type   : 'ERROR',
        message: 'Commande manquante. Utiliser : --login, --check, --sync, --upload, --quota, --rollback'
    }));
}
