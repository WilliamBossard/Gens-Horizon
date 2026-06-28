'use strict';
const fs = require('fs');
const Auth = require('../Auth');
class GoogleProvider {
    constructor(tokenData, credentials, tokenPath) {
        this._creds     = credentials;
        this._tokenData = tokenData;
        this._tokenPath = tokenPath;
    }
    async _getAccessToken() {
        if (!this._tokenData.access_token || !this._tokenData.expiry_date || Date.now() > this._tokenData.expiry_date - 60000) {
            await this._refreshToken();
        }
        return this._tokenData.access_token;
    }
    async _refreshToken() {
        if (!this._tokenData.refresh_token) throw new Error("Aucun refresh_token disponible.");
        const res = await fetch('https://oauth2.googleapis.com/token', {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams({
                client_id: this._creds.client_id,
                client_secret: this._creds.client_secret,
                refresh_token: this._tokenData.refresh_token,
                grant_type: 'refresh_token'
            })
        });
        if (!res.ok) {
            const txt = await res.text();
            throw new Error(`Erreur de rafraîchissement du token: ${txt}`);
        }
        const data = await res.json();
        const merged = { 
            ...this._tokenData, 
            access_token: data.access_token,
            expiry_date: Date.now() + data.expires_in * 1000
        };
        if (data.refresh_token) merged.refresh_token = data.refresh_token;
        this._tokenData = merged;
        try { Auth.encryptToken(this._tokenPath, merged); }
        catch (e) { process.stderr.write(`[google] Échec sauvegarde token rafraîchi : ${e.message}\n`); }
    }
    async _fetchDrive(path, options = {}) {
        let token = await this._getAccessToken();
        const doFetch = () => {
            const headers = { Authorization: `Bearer ${token}`, ...options.headers };
            return fetch(`https://www.googleapis.com/drive/v3${path}`, { ...options, headers });
        };
        let res = await doFetch();
        if (res.status === 401) {
            await this._refreshToken();
            token = await this._getAccessToken();
            res = await doFetch();
        }
        return res;
    }
    async listFiles(nameContains = '') {
        let files = [], pageToken = null;
        const MAX_PAGES = 20;
        let page = 0;
        do {
            const params = new URLSearchParams({
                spaces: 'appDataFolder',
                fields: 'nextPageToken, files(id, name, modifiedTime, size)',
                pageSize: '1000'
            });
            if (pageToken) params.append('pageToken', pageToken);
            if (nameContains) params.append('q', `name contains '${nameContains.replace(/'/g, "\\'")}'`);
            const res = await this._fetchDrive(`/files?${params.toString()}`);
            if (!res.ok) throw new Error(`[google] Erreur listFiles : ${await res.text()}`);
            const data = await res.json();
            if (data.files) files = files.concat(data.files);
            pageToken = data.nextPageToken;
            page++;
        } while (pageToken && page < MAX_PAGES);
        if (page >= MAX_PAGES && pageToken) {
            process.stderr.write(`[google] listFiles : limite de pagination atteinte (${MAX_PAGES} pages)\n`);
        }
        return files;
    }
    async downloadFile(fileId, destPath, onProgress, totalSize) {
        const token = await this._getAccessToken();
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(new Error('Google Drive: timeout téléchargement (10 min)')), 10 * 60_000);
        const buildUrl = (confirm) => {
            const url = new URL(`https://www.googleapis.com/drive/v3/files/${fileId}`);
            url.searchParams.set('alt', 'media');
            if (confirm) url.searchParams.set('acknowledgeAbuse', 'true');
            return url.toString();
        };
        let res = await fetch(buildUrl(false), {
            headers: { Authorization: `Bearer ${token}` },
            signal: controller.signal
        });
        if (res.status === 401) {
            await this._refreshToken();
            const newToken = await this._getAccessToken();
            res = await fetch(buildUrl(false), {
                headers: { Authorization: `Bearer ${newToken}` },
                signal: controller.signal
            });
        }
        // Google Drive renvoie une page HTML/JSON de confirmation pour les gros fichiers
        // Détection : Content-Type text/html ou application/json au lieu d'application/octet-stream
        const contentType = res.headers.get('content-type') || '';
        if (res.ok && (contentType.includes('text/html') || (contentType.includes('application/json') && !contentType.includes('octet')))) {
            process.stderr.write(`[google] Réponse de confirmation détectée, relance avec acknowledgeAbuse=true\n`);
            res = await fetch(buildUrl(true), {
                headers: { Authorization: `Bearer ${token}` },
                signal: controller.signal
            });
        }
        if (!res.ok) {
            clearTimeout(timeoutId);
            throw new Error(`[google] Erreur téléchargement : ${await res.text()}`);
        }
        const dest = fs.createWriteStream(destPath);
        let downloaded = 0, lastPct = -1;
        return new Promise((resolve, reject) => {
            const onError = (e) => {
                dest.destroy();
                try { fs.unlinkSync(destPath); } catch (_) {}
                reject(e);
            };
            const reader = res.body.getReader();
            const read = () => {
                reader.read().then(({ done, value }) => {
                    if (done) {
                        dest.end();
                        return;
                    }
                    downloaded += value.length;
                    if (totalSize > 0) {
                        const pct = Math.min(100, Math.round(downloaded / totalSize * 100));
                        if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                            onProgress && onProgress(pct);
                            lastPct = pct;
                        }
                    }
                    if (!dest.write(value)) {
                        dest.once('drain', read);
                    } else {
                        read();
                    }
                }).catch(onError);
            };
            dest.on('finish', () => {
                clearTimeout(timeoutId);
                if (totalSize > 0 && downloaded < totalSize) {
                    return onError(new Error(`Téléchargement incomplet: ${downloaded} / ${totalSize} bytes reçus. La connexion a probablement été coupée.`));
                }
                resolve();
            });
            dest.on('error', onError);
            read();
        });
    }
    async uploadZip(name, srcPath, existingId = null, onProgress = null) {
        const fileSize = fs.statSync(srcPath).size;
        const token = await this._getAccessToken();
        const metadata = { name, parents: existingId ? undefined : ['appDataFolder'] };
        const initMethod = existingId ? 'PATCH' : 'POST';
        const initUrl = existingId 
            ? `https://www.googleapis.com/upload/drive/v3/files/${existingId}?uploadType=resumable&fields=id,name,size`
            : `https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id,name,size`;
        let resInit = await fetch(initUrl, {
            method: initMethod,
            headers: {
                Authorization: `Bearer ${token}`,
                'Content-Type': 'application/json',
                'X-Upload-Content-Type': 'application/zip',
                'X-Upload-Content-Length': fileSize.toString()
            },
            body: JSON.stringify(metadata)
        });
        if (resInit.status === 401) {
            await this._refreshToken();
            const newToken = await this._getAccessToken();
            resInit = await fetch(initUrl, {
                method: initMethod,
                headers: {
                    Authorization: `Bearer ${newToken}`,
                    'Content-Type': 'application/json',
                    'X-Upload-Content-Type': 'application/zip',
                    'X-Upload-Content-Length': fileSize.toString()
                },
                body: JSON.stringify(metadata)
            });
        }
        if (!resInit.ok) throw new Error(`Erreur initiation upload: ${await resInit.text()}`);
        const uploadUrl = resInit.headers.get('location');
        if (!uploadUrl) throw new Error("Aucune URL d'upload retournée");
        return new Promise((resolve, reject) => {
            const https = require('https');
            const { URL } = require('url');
            const parsedUrl = new URL(uploadUrl);
            const req = https.request({
                method: 'PUT',
                hostname: parsedUrl.hostname,
                path: parsedUrl.pathname + parsedUrl.search,
                headers: { 'Content-Length': fileSize }
            }, (res) => {
                let data = '';
                res.on('data', chunk => data += chunk);
                res.on('end', () => {
                    if (res.statusCode === 200 || res.statusCode === 201) {
                        onProgress && onProgress(100);
                        try {
                            const parsedData = JSON.parse(data);
                            // Verifier si Google Drive a bien recu tout le fichier (API peut omettre size pour certains types, mais ZIP devrait l'avoir)
                            if (parsedData.size && parseInt(parsedData.size, 10) !== fileSize) {
                                return reject(new Error(`Upload corrompu: Google Drive a enregistré ${parsedData.size} octets, mais le fichier fait ${fileSize} octets.`));
                            }
                            resolve(parsedData);
                        } catch (err) {
                            reject(new Error(`Upload failed to parse response: ${err.message}`));
                        }
                    } else {
                        reject(new Error(`Upload failed: ${res.statusCode} ${data}`));
                    }
                });
            });
            req.on('error', reject);
            const readStream = fs.createReadStream(srcPath);
            readStream.on('error', (err) => { req.destroy(); reject(err); });
            let uploaded = 0;
            let lastPct = -1;
            readStream.on('data', chunk => {
                uploaded += chunk.length;
                if (onProgress && fileSize > 0) {
                    const pct = Math.min(100, Math.round((uploaded / fileSize) * 100));
                    if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) { 
                        onProgress(pct); 
                        lastPct = pct; 
                    }
                }
            });
            readStream.pipe(req);
        });
    }
    async uploadJSON(name, content, existingId = null) {
        const metadata = { name, parents: existingId ? undefined : ['appDataFolder'] };
        const fileContent = JSON.stringify(content);
        const boundary = 'foo_bar_baz';
        let body = `--${boundary}\r\n`;
        body += 'Content-Type: application/json; charset=UTF-8\r\n\r\n';
        body += JSON.stringify(metadata) + '\r\n';
        body += `--${boundary}\r\n`;
        body += 'Content-Type: application/json\r\n\r\n';
        body += fileContent + '\r\n';
        body += `--${boundary}--`;
        const method = existingId ? 'PATCH' : 'POST';
        const url = existingId 
            ? `/files/${existingId}?uploadType=multipart`
            : `/files?uploadType=multipart`;
        const res = await fetch(`https://www.googleapis.com/upload/drive/v3${url}`, {
            method,
            headers: {
                Authorization: `Bearer ${await this._getAccessToken()}`,
                'Content-Type': `multipart/related; boundary=${boundary}`
            },
            body
        });
        if (!res.ok) throw new Error(`[google] Erreur uploadJSON: ${await res.text()}`);
        return await res.json();
    }
    async downloadJSON(fileId) {
        const res = await this._fetchDrive(`/files/${fileId}?alt=media`);
        if (!res.ok) throw new Error(`[google] downloadJSON : erreur HTTP ${res.status}`);
        try {
            return await res.json();
        } catch (e) {
            throw new Error(`[google] downloadJSON : JSON invalide pour fileId=${fileId} — ${e.message}`);
        }
    }
    async deleteFile(fileId) {
        const res = await this._fetchDrive(`/files/${fileId}`, { method: 'DELETE' });
        if (!res.ok) throw new Error(`[google] deleteFile : erreur HTTP ${res.status}`);
    }
    async getQuota() {
        const res = await this._fetchDrive(`/about?fields=storageQuota`);
        if (!res.ok) throw new Error(`[google] getQuota : erreur HTTP ${res.status}`);
        const data = await res.json();
        const q = data.storageQuota || {};
        return {
            used   : parseInt(q.usage,        10) || 0,
            total  : parseInt(q.limit,         10) || 0,
            inDrive: parseInt(q.usageInDrive,  10) || 0,
        };
    }
}
module.exports = { GoogleProvider };