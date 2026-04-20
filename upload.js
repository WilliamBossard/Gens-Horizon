const fs = require('fs');
const { google } = require('googleapis');
const archiver = require('archiver');
const path = require('path');
const dns = require('dns').promises;
const { credentials } = require('./config');
const { getInstancesFolder, scanInstances } = require('./paths');
const { generateManifest, compareManifests } = require('./scanner');
const { getSecureToken } = require('./auth');

function getFolderSizeSync(dir) {
    let total = 0;
    try {
        const files = fs.readdirSync(dir);
        for (const f of files) {
            const p = path.join(dir, f);
            const stat = fs.statSync(p);
            total += stat.isDirectory() ? getFolderSizeSync(p) : stat.size;
        }
    } catch (e) {}
    return total;
}

async function upload() {
    try {
        await dns.lookup('google.com');

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        const tokenPath    = path.join(cwd, 'token.json');
        const syncInfoPath = path.join(cwd, 'last_sync.json');

        const args           = process.argv.slice(2);
        const force          = args.includes('--force');
        const COMMANDS = new Set(['sync', 'upload', 'check', 'login']);
        const targetInstance = args.find(a => !a.startsWith('--') && !COMMANDS.has(a));

        let settings = { syncMode: "SMART", autoSync: true, autoUpload: true };
        if (fs.existsSync(settingsPath)) {
            settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
        }

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

        let localInstances = [];
        if (targetInstance) {
            console.log(JSON.stringify({ type: "PROGRESS", step: "CHECKING", value: 0, instance: targetInstance }));
            const targetFolder = path.join(getInstancesFolder(), targetInstance);
            if (fs.existsSync(targetFolder)) {
                localInstances = [targetInstance];
            } else {
                console.log(JSON.stringify({ type: "ERROR", message: `Instance ${targetInstance} introuvable localement.` }));
                return;
            }
        } else {
            localInstances = scanInstances();
        }

        for (const inst of localInstances) {
            const folder       = path.join(getInstancesFolder(), inst);
            const manifestPath = path.join(cwd, `manifest_${inst}.json`);

            const currentM = await generateManifest(folder);
            const oldM     = fs.existsSync(manifestPath)
                ? JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
                : {};
            const diff = compareManifests(oldM, currentM);

            if (!diff.hasChanges && !force) {
                console.log(JSON.stringify({ type: "INFO", instance: inst, message: `Aucun changement pour ${inst}, upload ignoré.` }));
                continue;
            }

            if (settings.syncMode === "SMART") {
                const tempZip = path.join(cwd, `temp_${inst}.zip`);

                await new Promise((resolve, reject) => {
                    const output  = fs.createWriteStream(tempZip);
                    const archive = archiver('zip', { zlib: { level: 9 } });

                    const realTotalSize = getFolderSizeSync(folder);
                    let lastPercent = -1;

                    archive.on('progress', (p) => {
                        if (realTotalSize === 0) return;
                        let percent = Math.round((p.fs.processedBytes / realTotalSize) * 100);
                        if (percent > 100) percent = 100;
                        if (percent !== lastPercent && (percent >= lastPercent + 2 || percent === 100)) {
                            console.log(JSON.stringify({ type: "PROGRESS", step: "COMPRESSING", value: percent, instance: inst }));
                            lastPercent = percent;
                        }
                    });

                    archive.on('error', (err) => {
                        output.destroy();
                        try { if (fs.existsSync(tempZip)) fs.unlinkSync(tempZip); } catch (_) {}
                        reject(err);
                    });

                    output.on('close', resolve);
                    output.on('error', (err) => {
                        try { if (fs.existsSync(tempZip)) fs.unlinkSync(tempZip); } catch (_) {}
                        reject(err);
                    });

                    archive.pipe(output);
                    archive.directory(folder, false);
                    archive.finalize(); 
                });

                const oldFiles = await drive.files.list({
                    spaces: 'appDataFolder',
                    q: `name = 'GensHorizon_Backup_${inst}.zip'`
                });
                for (const f of oldFiles.data.files) await drive.files.delete({ fileId: f.id });

                
                const uploadRes = await drive.files.create({
                    resource: { name: `GensHorizon_Backup_${inst}.zip`, parents: ['appDataFolder'] },
                    media: { mimeType: 'application/zip', body: fs.createReadStream(tempZip) },
                    fields: 'id, modifiedTime'
                });

                const syncInfo      = fs.existsSync(syncInfoPath)
                    ? JSON.parse(fs.readFileSync(syncInfoPath, 'utf8'))
                    : {};
                syncInfo[inst]      = uploadRes.data.modifiedTime;
                fs.writeFileSync(syncInfoPath, JSON.stringify(syncInfo, null, 2));
                
                fs.unlinkSync(tempZip);
            }

            fs.writeFileSync(manifestPath, JSON.stringify(currentM, null, 2));
            console.log(JSON.stringify({ type: "SUCCESS", instance: inst }));
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

upload();