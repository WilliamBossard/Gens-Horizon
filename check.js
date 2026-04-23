/**
 * check.js
 * Vérifie si des mises à jour Cloud sont disponibles pour les instances locales.
 * Supporte les trois providers via la factory provider.js.
 */

'use strict';

const fs   = require('fs');
const path = require('path');
const dns  = require('dns').promises;

const { getInstancesFolder } = require('./paths');
const { getProvider }        = require('./provider');

const SYNC_INFO_FILE = path.join(process.cwd(), 'last_sync.json');
const SETTINGS_PATH  = path.join(process.cwd(), 'horizon_settings.json');

async function check() {
    try {
        try {
            await dns.lookup('google.com');
        } catch (dnsErr) {
            console.log(JSON.stringify({ status: 'OFFLINE', message: 'Internet indisponible.' }));
            return;
        }

        let settings = {};
        if (fs.existsSync(SETTINGS_PATH)) {
            try { settings = JSON.parse(fs.readFileSync(SETTINGS_PATH, 'utf8')); } catch (_) {}
        }

        const provider = await getProvider(settings);
        if (!provider) {
            console.log(JSON.stringify({ status: 'NOT_LOGGED_IN' }));
            process.exit(0);
        }

        const syncInfo   = fs.existsSync(SYNC_INFO_FILE)
            ? JSON.parse(fs.readFileSync(SYNC_INFO_FILE, 'utf8'))
            : {};

        const cloudFiles = await provider.listFiles('GensHorizon_');
        const cloudIndex = {};
        for (const f of cloudFiles) cloudIndex[f.name] = f;

        const cloudInstances = Object.keys(cloudIndex)
            .filter(n => n.startsWith('GensHorizon_Backup_'))
            .map(n => n.replace('GensHorizon_Backup_', '').replace('.zip', ''));

        let report = { status: 'UP_TO_DATE', updates: [] };

        for (const instName of cloudInstances) {
            const baseName  = `GensHorizon_Backup_${instName}.zip`;
            const baseFile  = cloudIndex[baseName];
            const cloudTime = new Date(baseFile.modifiedTime).getTime();

            const latestDeltaTime = Object.keys(cloudIndex)
                .filter(n => n.startsWith(`GensHorizon_Delta_${instName}_`))
                .map(n => {
                    const ts = parseInt(n.replace(`GensHorizon_Delta_${instName}_`, '').replace('.zip', ''), 10);
                    return isNaN(ts) ? 0 : ts;
                })
                .reduce((max, ts) => Math.max(max, ts), 0);

            const effectiveCloudTime = Math.max(cloudTime, latestDeltaTime);
            const lastSyncTime       = syncInfo[instName] ? new Date(syncInfo[instName]).getTime() : 0;

            const localPath = path.join(getInstancesFolder(), instName);
            const localExists = fs.existsSync(localPath);

            if (localExists && effectiveCloudTime > lastSyncTime) {
                report.status = 'UPDATE_AVAILABLE';
                report.updates.push(instName);
            }
        }

        console.log(JSON.stringify(report));

    } catch (e) {
        if (e.code === 'ENOTFOUND' || e.code === 'EAI_AGAIN' || e.code === 'ECONNREFUSED') {
            console.log(JSON.stringify({ status: 'OFFLINE', message: 'Internet indisponible.' }));
        } else {
            console.log(JSON.stringify({ status: 'ERROR', message: e.message }));
        }
    }
}

check();