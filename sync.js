const fs = require('fs');
const { google } = require('googleapis');
const AdmZip = require('adm-zip');
const path = require('path');
const dns = require('dns').promises;
const { credentials } = require('./config');
const { getInstancesFolder } = require('./paths');
const { getSecureToken } = require('./Auth');

async function syncAllInstances() {
    try {
        await dns.lookup('google.com');

        const cwd          = process.cwd();
        const tokenPath    = path.join(cwd, 'token.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');

        const args           = process.argv.slice(2);
        const force          = args.includes('--force');
        const isList         = args.includes('--list');
        const isDelete       = args.includes('--delete');
        const COMMANDS = new Set(['sync', 'upload', 'check', 'login']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        if (!fs.existsSync(tokenPath)) {
            console.log(JSON.stringify({ type: "ERROR", message: "Compte non lié" }));
            return;
        }

        const token = getSecureToken(tokenPath);
        const oauth2Client = new google.auth.OAuth2(
            credentials.web.client_id,
            credentials.web.client_secret
        );
        oauth2Client.setCredentials(token);
        const drive = google.drive({ version: 'v3', auth: oauth2Client });

        if (isList) {
try {
                let cloudFiles = [];
                let pageToken = null;
                do {
                    const res = await drive.files.list({ spaces: 'appDataFolder', fields: 'nextPageToken, files(id, name)', pageToken: pageToken, pageSize: 1000 });
                    if (res.data.files) cloudFiles = cloudFiles.concat(res.data.files);
                    pageToken = res.data.nextPageToken;
                } while (pageToken);

                const list = cloudFiles
                    .filter(f => f.name.startsWith("GensHorizon_Backup_"))
                    .map(f => f.name.replace('GensHorizon_Backup_', '').replace('.zip', ''));
                console.log(JSON.stringify({ type: "CLOUD_LIST", data: list }));
            } catch (e) {
                console.log(JSON.stringify({ type: "ERROR", message: "Erreur lecture Cloud : " + e.message }));
            }
            return;
        }

        if (isDelete && targetInstance) {
            const res = await drive.files.list({
                spaces: 'appDataFolder',
                q: `name = 'GensHorizon_Backup_${targetInstance}.zip'`
            });
            for (const f of res.data.files) await drive.files.delete({ fileId: f.id });

            const manifestPath = path.join(cwd, `manifest_${targetInstance}.json`);
            if (fs.existsSync(manifestPath)) fs.unlinkSync(manifestPath);

            if (fs.existsSync(syncInfoPath)) {
                let syncState = JSON.parse(fs.readFileSync(syncInfoPath, 'utf8'));
                if (syncState[targetInstance]) {
                    delete syncState[targetInstance];
                    fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));
                }
            }

            console.log(JSON.stringify({ type: "SUCCESS", instance: targetInstance, message: "Supprimé du cloud" }));
            return;
        }

        let syncState = fs.existsSync(syncInfoPath)
            ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8'))
            : {};

        const driveQuery = targetInstance
            ? `name = 'GensHorizon_Backup_${targetInstance}.zip'`
            : `name contains 'GensHorizon_Backup_'`;

        if (targetInstance) {
            console.log(JSON.stringify({ type: "PROGRESS", step: "CHECKING", value: 0, instance: targetInstance }));
        }

let cloudFiles = [];
        let pageToken = null;
        do {
            const res = await drive.files.list({
                spaces: 'appDataFolder',
                q: driveQuery,
                fields: 'nextPageToken, files(id, name, modifiedTime, size)',
                pageToken: pageToken,
                pageSize: 1000
            });
            if (res.data.files) cloudFiles = cloudFiles.concat(res.data.files);
            pageToken = res.data.nextPageToken;
        } while (pageToken);

        if (targetInstance && cloudFiles.length === 0) {
            console.log(JSON.stringify({
                type: "INFO",
                instance: targetInstance,
                message: `[${targetInstance}] n'existe pas encore sur le Cloud. Lancement rapide.`
            }));
            return;
        }

        for (const file of cloudFiles) {
            if (!file.name.startsWith("GensHorizon_Backup_")) continue;

            const instName  = file.name.replace('GensHorizon_Backup_', '').replace('.zip', '');
            if (targetInstance && instName !== targetInstance) continue;

            const cloudDate = new Date(file.modifiedTime);
            const localDate = syncState[instName] ? new Date(syncState[instName]) : new Date(0);

            if (cloudDate > localDate || force) {
                const tempZip = path.join(cwd, `download_${instName}.zip`);

                const dest    = fs.createWriteStream(tempZip);
                const fileRes = await drive.files.get(
                    { fileId: file.id, alt: 'media' },
                    { responseType: 'stream' }
                );

                const totalSize = parseInt(file.size, 10) || 0;
                let downloaded  = 0;
                let lastPercent = -1;

                await new Promise((resolve, reject) => {
                    fileRes.data.on('data', (chunk) => {
                        downloaded += chunk.length;
                        if (totalSize > 0) {
                            let percent = Math.round((downloaded / totalSize) * 100);
                            if (percent > 100) percent = 100;
                            if (percent !== lastPercent && (percent >= lastPercent + 2 || percent === 100)) {
                                console.log(JSON.stringify({ type: "PROGRESS", step: "DOWNLOADING", value: percent, instance: instName }));
                                lastPercent = percent;
                            }
                        }
                    });

                    fileRes.data.pipe(dest);

                    fileRes.data.on('end', () => {
                        dest.end(() => {
                            try {
                                console.log(JSON.stringify({ type: "PROGRESS", step: "DOWNLOADING", value: 100, instance: instName }));

                                const targetPath = path.join(getInstancesFolder(), instName);
                                if (!fs.existsSync(targetPath)) fs.mkdirSync(targetPath, { recursive: true });

                                const zip = new AdmZip(tempZip);
                                zip.extractAllTo(targetPath, true);

                                syncState[instName] = file.modifiedTime;
                                fs.writeFileSync(syncInfoPath, JSON.stringify(syncState, null, 2));
                                fs.unlinkSync(tempZip);
                                resolve();
                            } catch (e) {
                                try { if (fs.existsSync(tempZip)) fs.unlinkSync(tempZip); } catch (_) {}
                                reject(e);
                            }
                        });
                    });

                    fileRes.data.on('error', (e) => {
                        dest.destroy();
                        try { if (fs.existsSync(tempZip)) fs.unlinkSync(tempZip); } catch (_) {}
                        reject(e);
                    });
                });

                console.log(JSON.stringify({ type: "SUCCESS", instance: instName }));
            } else {
                console.log(JSON.stringify({
                    type: "INFO",
                    instance: instName,
                    message: `[${instName}] est déjà à jour.`
                }));
            }
        }
} catch (e) {
        if (e.message && (e.message.includes("invalid_grant") || e.message.includes("invalid_token"))) {
            const tokenPath = path.join(process.cwd(), 'token.json');
            if (fs.existsSync(tokenPath)) fs.unlinkSync(tokenPath);
            console.log(JSON.stringify({ type: "ERROR", message: "Session expirée. Veuillez ré-associer votre compte Google." }));
        } else {
            console.log(JSON.stringify({ type: "ERROR", message: "Crash : " + e.message }));
        }
    }
}

syncAllInstances();