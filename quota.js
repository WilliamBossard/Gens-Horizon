'use strict';

const fs   = require('fs');
const path = require('path');
const dns  = require('dns').promises;

const { getProvider } = require('./provider');

async function checkConnectivity() {
    const hosts = ['1.1.1.1', 'google.com', 'microsoft.com'];
    for (const host of hosts) {
        try { await dns.lookup(host); return true; } catch (_) {}
    }
    return false;
}

async function quota() {
    try {
        const online = await checkConnectivity();
        if (!online) {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible.' }));
            return;
        }

        const cwd          = process.cwd();
        const settingsPath = path.join(cwd, 'horizon_settings.json');
        let settings = {};
        if (fs.existsSync(settingsPath)) {
            try { settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8')); } catch (_) {}
        }

        const provider = await getProvider(settings, cwd);
        if (!provider) {
            console.log(JSON.stringify({ type: 'ERROR', message: "Compte non lié. Lance --login d'abord." }));
            return;
        }

        const quotaInfo = await provider.getQuota();

        const cloudFiles  = await provider.listFiles('GensHorizon_');
        const horizonUsed = cloudFiles.reduce((s, f) => s + (parseInt(f.size, 10) || 0), 0);

        const instanceMap = {};
        const deltaCountMap = {};

        for (const f of cloudFiles) {
            let instName = null;
            let isDelta  = false;

            if (f.name.startsWith('GensHorizon_Backup_')) {
                instName = f.name.replace('GensHorizon_Backup_', '').replace('.zip', '');
            } else if (f.name.startsWith('GensHorizon_Delta_')) {
                const body  = f.name.replace('GensHorizon_Delta_', '').replace('.zip', '');
                const parts = body.split('_');
                parts.pop();
                instName = parts.join('_');
                isDelta  = true;
            } else if (f.name.startsWith('GensHorizon_Manifest_')) {
                instName = f.name.replace('GensHorizon_Manifest_', '').replace('.json', '');
            }

            if (instName) {
                instanceMap[instName]  = (instanceMap[instName]  || 0) + (parseInt(f.size, 10) || 0);
                if (isDelta) deltaCountMap[instName] = (deltaCountMap[instName] || 0) + 1;
            }
        }

        const instances = Object.entries(instanceMap).map(([name, bytes]) => ({
            name,
            bytes,
            deltaCount: deltaCountMap[name] || 0,
        })).sort((a, b) => b.bytes - a.bytes);

        console.log(JSON.stringify({
            type        : 'QUOTA',
            provider    : settings.provider || 'google',
            totalBytes  : quotaInfo.total,
            usedBytes   : quotaInfo.used,
            horizonBytes: horizonUsed,
            instances,
        }));

    } catch (e) {
        if (e.code === 'ENOTFOUND' || e.code === 'EAI_AGAIN') {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible.' }));
        } else {
            console.log(JSON.stringify({ type: 'ERROR', message: e.message }));
        }
    }
}

quota();
