'use strict';

const fs = require('fs');
const path = require('path');
const { Readable } = require('stream');
const { google } = require('googleapis');
const { credentials } = require('../config');
const Auth = require('../Auth'); 

const TOKEN_PATH = path.join(process.cwd(), 'token_google.json');

function getAuthUrl() {
    const oauth2 = new google.auth.OAuth2(
        credentials.google.client_id,
        credentials.google.client_secret,
        credentials.google.redirect_uri
    );
    return oauth2.generateAuthUrl({
        access_type: 'offline',
        scope: ['https://www.googleapis.com/auth/drive.appdata'],
        prompt: 'consent'
    });
}

async function handleAuthCode(code) {
    const oauth2 = new google.auth.OAuth2(
        credentials.google.client_id,
        credentials.google.client_secret,
        credentials.google.redirect_uri
    );
    const { tokens } = await oauth2.getToken(code);
    
    Auth.encryptToken(TOKEN_PATH, tokens); 
    
    return tokens;
}

class GoogleProvider {
    constructor(tokenData, credentials) {
        this._creds  = credentials;
        this._tokenData = tokenData;

        const oauth2 = new google.auth.OAuth2(credentials.client_id, credentials.client_secret);
        oauth2.setCredentials(tokenData);

        oauth2.on('tokens', (newTokens) => {
            const merged = { ...this._tokenData, ...newTokens };
            this._tokenData = merged;
            try {
                Auth.encryptToken(TOKEN_PATH, merged);
            } catch (e) {
                process.stderr.write(`[google] Échec sauvegarde token rafraîchi : ${e.message}\n`);
            }
        });

        this._drive = google.drive({ version: 'v3', auth: oauth2 });
    }

    async listFiles(nameContains = '') {
        let files = [], pageToken = null;
        do {
            const params = {
                spaces   : 'appDataFolder',
                fields   : 'nextPageToken, files(id, name, modifiedTime, size)',
                pageSize : 1000,
                ...(pageToken     && { pageToken }),
                ...(nameContains  && { q: `name contains '${nameContains}'` })
            };
            const res = await this._drive.files.list(params);
            if (res.data.files) files = files.concat(res.data.files);
            pageToken = res.data.nextPageToken;
        } while (pageToken);
        return files;
    }

    async downloadFile(fileId, destPath, onProgress, totalSize) {
        const dest = fs.createWriteStream(destPath);
        const res  = await this._drive.files.get({ fileId, alt: 'media' }, { responseType: 'stream' });
        let downloaded = 0, lastPct = -1;

        return new Promise((resolve, reject) => {
            res.data.on('data', chunk => {
                downloaded += chunk.length;
                if (totalSize > 0) {
                    const pct = Math.min(100, Math.round(downloaded / totalSize * 100));
                    if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                        onProgress && onProgress(pct);
                        lastPct = pct;
                    }
                }
            });
            res.data.pipe(dest);
            res.data.on('end',   () => dest.end(resolve));
            res.data.on('error', e  => { dest.destroy(); reject(e); });
        });
    }

    async uploadZip(name, srcPath, existingId = null, onProgress = null) {
        const fileSize = fs.statSync(srcPath).size;
        const media = { mimeType: 'application/zip', body: fs.createReadStream(srcPath) };
        
        let lastPct = -1;
        const progressOptions = {
            onUploadProgress: evt => {
                if (onProgress && fileSize > 0) {
                    const pct = Math.min(100, Math.round((evt.bytesRead / fileSize) * 100));
                    if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                        onProgress(pct);
                        lastPct = pct;
                    }
                }
            }
        };

        if (existingId) {
            const res = await this._drive.files.update(
                { fileId: existingId, media, fields: 'id, modifiedTime' },
                progressOptions
            );
            onProgress && onProgress(100);
            return res.data;
        }

        const res = await this._drive.files.create(
            { resource: { name, parents: ['appDataFolder'] }, media, fields: 'id, modifiedTime' },
            progressOptions
        );
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
        const res = await this._drive.files.create({
            resource: { name, parents: ['appDataFolder'] },
            media,
            fields: 'id, modifiedTime'
        });
        return res.data;
    }

    async downloadJSON(fileId) {
        const res = await this._drive.files.get({ fileId, alt: 'media' });
        return typeof res.data === 'string' ? JSON.parse(res.data) : res.data;
    }

    async deleteFile(fileId) {
        await this._drive.files.delete({ fileId });
    }
}

module.exports = { GoogleProvider, getAuthUrl, handleAuthCode };
