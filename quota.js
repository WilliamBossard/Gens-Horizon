'use strict';

const fs   = require('fs');
const path = require('path');

const { getProvider } = require('./provider');
const { getHorizonDataDir } = require('./paths');
const { checkConnectivity, setupProcessHandlers } = require('./utils');
const { withRetry } = require('./retry');

setupProcessHandlers();

async function quota() {
    try {
        const online = await checkConnectivity();
        if (!online) {
            console.log(JSON.stringify({ type: 'OFFLINE', message: 'Internet indisponible.' }));
            return;
        }

        const dataDir      = getHorizonDataDir();
        const settingsPath = path.join(dataDir, 'horizon_settings.json');
        let settings = {};
        if (fs.existsSync(settingsPath)) {
            try { settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8')); } catch (_) {}
        }

        const retryOpts = { maxRetries: settings.maxRetries || 3, baseDelay: settings.retryBaseDelay || 1500 };

        const provider = await getProvider(settings);
        if (!provider) {
            console.log(JSON.stringify({
                type: 'ERROR',
                errorCode: 'AUTH_EXPIRED',
                message: "Session expirée. Veuillez lier à nouveau votre compte depuis les paramètres."
            }));
            return;
        }

        const quotaInfo = await withRetry(() => provider.getQuota(), { ...retryOpts, label: 'getQuota' });

        const cloudFiles  = await withRetry(() => provider.listFiles('GensHorizon_'), { ...retryOpts, label: 'listFiles' });
        const horizonUsed = cloudFiles.reduce((s, f) => s + (parseInt(f.size, 10) || 0), 0);

        const instanceMap   = {};
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
            } else if (f.name.startsWith('GensHorizon_Meta_')) {
                instName = f.name.replace('GensHorizon_Meta_', '').replace('.json', '');
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
