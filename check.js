const fs = require('fs');
const { google } = require('googleapis');
const path = require('path');
const dns = require('dns').promises;
const { credentials } = require('./config');
const { getInstancesFolder } = require('./paths');
const { getSecureToken } = require('./auth');

const TOKEN_PATH     = path.join(process.cwd(), 'token.json');
const SYNC_INFO_FILE = path.join(process.cwd(), 'last_sync.json');

async function getDriveClient() {
    if (!fs.existsSync(TOKEN_PATH)) {
        console.log(JSON.stringify({ status: "NOT_LOGGED_IN" }));
        process.exit(0);
    }

    const token = getSecureToken(TOKEN_PATH);
    if (!token) {
        console.log(JSON.stringify({ status: "NOT_LOGGED_IN" }));
        process.exit(0);
    }

    const oauth2Client = new google.auth.OAuth2(
        credentials.web.client_id,
        credentials.web.client_secret
    );
    oauth2Client.setCredentials(token);
    return google.drive({ version: 'v3', auth: oauth2Client });
}

async function check() {
    try {
        await dns.lookup('google.com');

        const drive = await getDriveClient();
        const syncInfo = fs.existsSync(SYNC_INFO_FILE)
            ? JSON.parse(fs.readFileSync(SYNC_INFO_FILE, 'utf8'))
            : {};

        const res = await drive.files.list({
            spaces: 'appDataFolder',
            fields: 'files(id, name, modifiedTime)'
        });
        const cloudFiles = res.data.files;

        let report = { status: "UP_TO_DATE", updates: [] };

        for (const file of cloudFiles) {
            if (!file.name.startsWith("GensHorizon_Backup_")) continue;
            const instName = file.name.replace('GensHorizon_Backup_', '').replace('.zip', '');

            const cloudTime    = new Date(file.modifiedTime).getTime();
            const lastSyncTime = syncInfo[instName] ? new Date(syncInfo[instName]).getTime() : 0;

            const localPath = path.join(getInstancesFolder(), instName);
            let localModifiedTime = 0;
            if (fs.existsSync(localPath)) {
                localModifiedTime = fs.statSync(localPath).mtime.getTime();
            }

            if (cloudTime > lastSyncTime && localModifiedTime > lastSyncTime + 5000) {
                report.status = "CONFLICT";
                report.instance = instName;
                break;
            }
            else if (cloudTime > lastSyncTime) {
                report.status = "UPDATE_AVAILABLE";
                report.updates.push(instName);
            }
        }

        console.log(JSON.stringify(report));

    } catch (e) {
        console.log(JSON.stringify({ status: "OFFLINE", message: "Internet indisponible" }));
    }
}

check();