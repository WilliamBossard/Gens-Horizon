'use strict';

const fs = require('fs');
const path = require('path');
const { Readable } = require('stream');
const { google } = require('googleapis');
const Auth = require('../Auth');

class GoogleProvider {
    constructor(tokenData, credentials, tokenPath) {
        this._creds     = credentials;
        this._tokenData = tokenData;
        this._tokenPath = tokenPath;

        const oauth2 = new google.auth.OAuth2(credentials.client_id, credentials.client_secret);
        oauth2.setCredentials(tokenData);

        oauth2.on('tokens', (newTokens) => {
            const merged = { ...this._tokenData, ...newTokens };
            this._tokenData = merged;
            try { Auth.encryptToken(this._tokenPath, merged); }
            catch (e) { process.stderr.write(`[google] Échec sauvegarde token rafraîchi : ${e.message}\n`); }
        });

        this._drive = google.drive({ version: 'v3', auth: oauth2 });
    }

    async listFiles(nameContains = '') {
        let files = [], pageToken = null;
        const MAX_PAGES = 20;
        let page = 0;
        do {
            const params = {
                spaces  : 'appDataFolder',
                fields  : 'nextPageToken, files(id, name, modifiedTime, size)',
                pageSize: 1000,
                ...(pageToken && { pageToken }),
                ...(nameContains && { q: `name contains '${nameContains.replace(/'/g, "\\'")}'` }),
            };
            const res = await this._drive.files.list(params);
            if (res.data.files) files = files.concat(res.data.files);
            pageToken = res.data.nextPageToken;
            page++;
        } while (pageToken && page < MAX_PAGES);

        if (page >= MAX_PAGES && pageToken) {
            process.stderr.write(`[google] listFiles : limite de pagination atteinte (${MAX_PAGES} pages)\n`);
        }
        return files;
    }

    async downloadFile(fileId, destPath, onProgress, totalSize) {
        const controller  = new AbortController();
        const timeoutId   = setTimeout(() => controller.abort(new Error('Google Drive: timeout téléchargement (10 min)')), 10 * 60_000);
        const dest        = fs.createWriteStream(destPath);
        let downloaded    = 0, lastPct = -1;

        try {
            const res = await this._drive.files.get(
                { fileId, alt: 'media' },
                { responseType: 'stream', signal: controller.signal }
            );

            return await new Promise((resolve, reject) => {
                const onError = (e) => {
                    res.data.destroy();
                    dest.destroy();
                    try { fs.unlinkSync(destPath); } catch (_) {}
                    reject(e);
                };
                res.data.on('data', chunk => {
                    downloaded += chunk.length;
                    if (totalSize > 0) {
                        const pct = Math.min(100, Math.round(downloaded / totalSize * 100));
                        if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                            onProgress && onProgress(pct);
                            lastPct = pct;
                        }
                    }
                    if (!dest.write(chunk)) {
                        res.data.pause();
                        dest.once('drain', () => res.data.resume());
                    }
                });
                res.data.on('end', () => dest.end());
                res.data.on('error', onError);
                dest.on('finish', resolve);
                dest.on('error', onError);
            });
        } finally {
            clearTimeout(timeoutId);
        }
    }

    async uploadZip(name, srcPath, existingId = null, onProgress = null) {
        const fileSize = fs.statSync(srcPath).size;
        const media    = { mimeType: 'application/zip', body: fs.createReadStream(srcPath) };
        let lastPct    = -1;

        const progressOptions = {
            onUploadProgress: evt => {
                if (onProgress && fileSize > 0) {
                    const pct = Math.min(100, Math.round((evt.bytesRead / fileSize) * 100));
                    if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) { onProgress(pct); lastPct = pct; }
                }
            },
        };

        if (existingId) {
            const res = await this._drive.files.update({ fileId: existingId, media, fields: 'id, modifiedTime' }, progressOptions);
            onProgress && onProgress(100);
            return res.data;
        }
        const res = await this._drive.files.create({ resource: { name, parents: ['appDataFolder'] }, media, fields: 'id, modifiedTime' }, progressOptions);
        onProgress && onProgress(100);
        return res.data;
    }

    async uploadJSON(name, content, existingId = null) {
        const body  = Readable.from([JSON.stringify(content, null, 2)]);
        const media = { mimeType: 'application/json', body };
        if (existingId) {
            const res = await this._drive.files.update({ fileId: existingId, media, fields: 'id, modifiedTime' });
            return res.data;
        }
        const res = await this._drive.files.create({ resource: { name, parents: ['appDataFolder'] }, media, fields: 'id, modifiedTime' });
        return res.data;
    }

    async downloadJSON(fileId) {
        const res  = await this._drive.files.get({ fileId, alt: 'media' });
        const data = res.data;
        if (data && typeof data === 'object' && !Buffer.isBuffer(data)) return data;
        const str = Buffer.isBuffer(data) ? data.toString('utf8') : String(data);
        try {
            return JSON.parse(str);
        } catch (e) {
            throw new Error(`[google] downloadJSON : JSON invalide pour fileId=${fileId} — ${e.message}`);
        }
    }

    async deleteFile(fileId) {
        await this._drive.files.delete({ fileId });
    }

    async getQuota() {
        const res = await this._drive.about.get({ fields: 'storageQuota' });
        const q   = res.data.storageQuota || {};
        return {
            used   : parseInt(q.usage,        10) || 0,
            total  : parseInt(q.limit,         10) || 0,
            inDrive: parseInt(q.usageInDrive,  10) || 0,
        };
    }
}

module.exports = { GoogleProvider };