'use strict';

const fs    = require('fs');
const https = require('https');
const path  = require('path');
const os    = require('os');
const { credentials } = require('../config');
const Auth = require('../Auth');
const GRAPH_HOST = 'graph.microsoft.com';
const APP_ROOT   = '/v1.0/me/drive/special/approot';
const { registerTemp, unregisterTemp } = require('../utils');

function graphRequest(method, graphPath, accessToken, body = null) {
    const bodyBuf = body ? Buffer.from(JSON.stringify(body)) : null;
    return new Promise((resolve, reject) => {
        const headers = { 'Authorization': `Bearer ${accessToken}`, 'Accept': 'application/json' };
        if (bodyBuf) { headers['Content-Type'] = 'application/json'; headers['Content-Length'] = bodyBuf.length; }
        const req = https.request({ hostname: GRAPH_HOST, path: graphPath, method, headers }, (res) => {
            const chunks = [];
            res.on('data', c => chunks.push(c));
            res.on('end', () => {
                const raw = Buffer.concat(chunks).toString('utf8').trim();
                let parsed = {};
                if (raw) { try { parsed = JSON.parse(raw); } catch (_) { parsed = {}; } }
                resolve({ statusCode: res.statusCode, body: parsed });
            });
        });
        req.setTimeout(30_000, () => { req.destroy(new Error('OneDrive: timeout réseau graphRequest (30s)')); });
        req.on('error', reject);
        if (bodyBuf) req.write(bodyBuf);
        req.end();
    });
}

class OneDriveProvider {
    constructor(tokenData, credentials, tokenPath) {
        this._token   = tokenData.access_token;
        this._refresh = tokenData.refresh_token;
        this._creds   = credentials;
        this._tokenPath = tokenPath;
    }

    async _refreshToken() {
        const scope = this._creds.scope || 'Files.ReadWrite offline_access';
        const params = new URLSearchParams({ grant_type: 'refresh_token', refresh_token: this._refresh, client_id: this._creds.client_id, scope });
        if (this._creds.client_secret) params.set('client_secret', this._creds.client_secret);
        const buf = Buffer.from(params.toString());
        const res = await new Promise((resolve, reject) => {
            const req = https.request({
                hostname: 'login.microsoftonline.com', path: '/common/oauth2/v2.0/token', method: 'POST',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Content-Length': buf.length },
            }, (r) => {
                const chunks = [];
                r.on('data', c => chunks.push(c));
                r.on('end', () => { try { resolve({ statusCode: r.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }); } catch (e) { reject(e); } });
            });
            req.setTimeout(20_000, () => { req.destroy(new Error('OneDrive: timeout refresh token (20s)')); });
            req.on('error', reject); req.write(buf); req.end();
        });
        if (!res.body.access_token) throw new Error('OneDrive token refresh failed: ' + (res.body.error_description || res.body.error || res.statusCode));
        this._token = res.body.access_token;
        if (res.body.refresh_token) this._refresh = res.body.refresh_token;
        Auth.encryptToken(this._tokenPath, { access_token: this._token, refresh_token: this._refresh, token_type: res.body.token_type || 'Bearer' }); 
    }

    async _call(method, graphPath, body = null, _retried = false) {
        let res = await graphRequest(method, graphPath, this._token, body);
        if (res.statusCode === 401 && !_retried) { await this._refreshToken(); return this._call(method, graphPath, body, true); }
        if (res.statusCode === 401) throw new Error('OneDrive : token invalide après refresh (accès révoqué ?)');
        if (res.statusCode >= 400) {
            const msg = res.body?.error?.message || res.body?.message || JSON.stringify(res.body).slice(0, 300);
            throw new Error(`OneDrive API ${res.statusCode}: ${msg}`);
        }
        return res;
    }

    async listFiles(nameContains = '') {
        let items = [];
        let nextPath = `${APP_ROOT}/children?$top=1000`;

        while (nextPath) {
            const res = await this._call('GET', nextPath);
            items = items.concat(res.body.value || []);
            const nextLink = res.body['@odata.nextLink'];
            if (nextLink) {
                try { nextPath = new URL(nextLink).pathname + new URL(nextLink).search; }
                catch (_) { nextPath = null; }
            } else {
                nextPath = null;
            }
        }

        return items
            .filter(i => i.file)
            .filter(i => !nameContains || i.name.includes(nameContains))
            .map(i => ({ id: i.id, name: i.name, modifiedTime: i.lastModifiedDateTime, size: i.size }));
    }

    async _uploadSession(name, srcPath, onProgress) {
        const CHUNK = 10 * 1024 * 1024;
        const total = fs.statSync(srcPath).size;

        if (total === 0) {
            return this.uploadZip(name, srcPath, null, onProgress);
        }

        let offset = 0, lastPct = -1, result = null;

        const sessionRes = await this._call('POST', `${APP_ROOT}:/${encodeURIComponent(name)}:/createUploadSession`, { item: { '@microsoft.graph.conflictBehavior': 'replace' } });
        if (sessionRes.statusCode >= 400 || !sessionRes.body.uploadUrl) throw new Error('OneDrive upload session failed');
        const uploadUrl = new URL(sessionRes.body.uploadUrl);
const fd = fs.openSync(srcPath, 'r');

        try {
            while (offset < total) {
                const end         = Math.min(offset + CHUNK, total);
                const bytesToRead = end - offset;
                const buffer      = Buffer.alloc(bytesToRead);
                fs.readSync(fd, buffer, 0, bytesToRead, offset);
                const isLast = end === total;

                const res = await new Promise((resolve, reject) => {
                    const req = https.request({
                        hostname: uploadUrl.hostname, path: uploadUrl.pathname + uploadUrl.search, method: 'PUT',
                        headers: { 'Content-Length': bytesToRead, 'Content-Range': `bytes ${offset}-${end - 1}/${total}`, 'Content-Type': 'application/octet-stream' },
                    }, (r) => {
                        const chunks = [];
                        r.on('data', c => chunks.push(c));
                        r.on('end', () => {
                            let parsed = {};
                            if (chunks.length > 0) {
                                try { parsed = JSON.parse(Buffer.concat(chunks).toString()); }
                                catch (_) { parsed = {}; }
                            }
                            resolve({ statusCode: r.statusCode, body: parsed });
                        });
                    });
                    req.setTimeout(120_000, () => {
                        req.destroy(new Error(`OneDrive: timeout chunk PUT offset=${offset} (120s)`));
                    });
                    req.on('error', reject); req.write(buffer); req.end();
                });

                if (res.statusCode >= 400) throw new Error(`OneDrive chunk error ${res.statusCode}`);
                offset = end;
                if (onProgress && total > 0) {
                    const pct = isLast ? 100 : Math.min(99, Math.round(offset / total * 100));
                    if (pct !== lastPct) { onProgress(pct); lastPct = pct; }
                }
                if (isLast && (res.statusCode === 200 || res.statusCode === 201)) {
                    result = { id: res.body.id, modifiedTime: res.body.lastModifiedDateTime };
                }
            }
        } catch (error) {
            try {
                await new Promise((resolve) => {
                    const req = https.request({
                        hostname: uploadUrl.hostname,
                        path: uploadUrl.pathname + uploadUrl.search,
                        method: 'DELETE'
                    }, resolve);
                    req.setTimeout(15_000, () => { req.destroy(); resolve(); }); // best-effort
                    req.on('error', resolve);
                    req.end();
                });
            } catch (_) {}

            throw error;
        } finally {
            fs.closeSync(fd);
        }
        return result;
    }

    async uploadZip(name, srcPath, existingId = null, onProgress = null) {
        const stats = fs.statSync(srcPath);
        const SIMPLE_LIMIT = 4 * 1024 * 1024;
        if (stats.size > SIMPLE_LIMIT) return this._uploadSession(name, srcPath, onProgress);

        const content = fs.readFileSync(srcPath);
        const _doUpload = async () => new Promise((resolve, reject) => {
            const req = https.request({
                hostname: GRAPH_HOST, path: `${APP_ROOT}:/${encodeURIComponent(name)}:/content`, method: 'PUT',
                headers: { 'Authorization': `Bearer ${this._token}`, 'Content-Type': 'application/octet-stream', 'Content-Length': content.length },
            }, (r) => {
                const chunks = [];
                r.on('data', c => chunks.push(c));
                r.on('end', () => resolve({ statusCode: r.statusCode, body: chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {} }));
            });
            req.setTimeout(60_000, () => { req.destroy(new Error('OneDrive: timeout uploadZip simple (60s)')); });
            req.on('error', reject); req.write(content); req.end();
        });
        let res = await _doUpload();
        if (res.statusCode === 401) { await this._refreshToken(); res = await _doUpload(); }
        if (res.statusCode >= 400) {
            throw new Error(`OneDrive uploadZip error ${res.statusCode}: ${res.body?.error?.message || res.body?.message || ''}`);
        }
        onProgress && onProgress(100);
        return { id: res.body.id, modifiedTime: res.body.lastModifiedDateTime };
    }

    async uploadJSON(name, content, existingId = null) {
        const buf = Buffer.from(JSON.stringify(content, null, 2));
        const _doUpload = async () => new Promise((resolve, reject) => {
            const req = https.request({
                hostname: GRAPH_HOST, path: `${APP_ROOT}:/${encodeURIComponent(name)}:/content`, method: 'PUT',
                headers: { 'Authorization': `Bearer ${this._token}`, 'Content-Type': 'application/json', 'Content-Length': buf.length },
            }, (r) => {
                const chunks = [];
                r.on('data', c => chunks.push(c));
                r.on('end', () => { try { resolve({ statusCode: r.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }); } catch (e) { reject(e); } });
            });
            req.setTimeout(30_000, () => { req.destroy(new Error('OneDrive: timeout uploadJSON (30s)')); });
            req.on('error', reject); req.write(buf); req.end();
        });
        let res = await _doUpload();
        if (res.statusCode === 401) { await this._refreshToken(); res = await _doUpload(); }
        if (res.statusCode >= 400) throw new Error(`OneDrive uploadJSON error ${res.statusCode}: ${res.body?.message || ''}`);
        return { id: res.body.id, modifiedTime: res.body.lastModifiedDateTime };
    }

    async _getDownloadUrl(fileId) {
        return new Promise((resolve, reject) => {
            const req = https.request({
                hostname: GRAPH_HOST, path: `/v1.0/me/drive/items/${fileId}/content`,
                method: 'GET', headers: { 'Authorization': `Bearer ${this._token}` },
            }, (res) => {
                res.resume();
                if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) resolve(res.headers.location);
                else reject(Object.assign(new Error(`OneDrive download: statut inattendu ${res.statusCode}`), { statusCode: res.statusCode }));
            });
            req.setTimeout(20_000, () => { req.destroy(new Error('OneDrive: timeout _getDownloadUrl (20s)')); });
            req.on('error', reject); req.end();
        });
    }

    async downloadFile(fileId, destPath, onProgress, totalSize) {
        let redirectUrl;
        try {
            redirectUrl = await this._getDownloadUrl(fileId);
        } catch (e) {
            if (e.statusCode === 401) { await this._refreshToken(); redirectUrl = await this._getDownloadUrl(fileId); }
            else throw e;
        }
        const loc = new URL(redirectUrl);
        await new Promise((resolve, reject) => {
            const dest = fs.createWriteStream(destPath);
            let downloaded = 0, lastPct = -1;
            const req = https.request({ hostname: loc.hostname, path: loc.pathname + loc.search, method: 'GET' }, (res) => {
            if (res.statusCode < 200 || res.statusCode >= 300) {
                res.resume();
                dest.destroy();
                try { fs.unlinkSync(destPath); } catch (_) {}
                return reject(Object.assign(
                    new Error(`OneDrive download HTTP ${res.statusCode}`),
                    { statusCode: res.statusCode }
                ));
            }
            
            const onError = (e) => {
                res.destroy();
                dest.destroy();
                try { fs.unlinkSync(destPath); } catch (_) {}
                reject(e);
            };
            res.on('data', chunk => {
                downloaded += chunk.length;
                if (totalSize > 0 && onProgress) {
                    const pct = Math.min(100, Math.round(downloaded / totalSize * 100));
                    if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) { onProgress(pct); lastPct = pct; }
                }
                if (!dest.write(chunk)) {
                    res.pause();
                    dest.once('drain', () => res.resume());
                }
            });
            res.on('end', () => dest.end());
            res.on('error', onError);
            dest.on('finish', resolve);
            dest.on('error', onError);
            });
            req.on('error', e => { dest.destroy(); reject(e); });
            req.end();
        });
    }

    async downloadJSON(fileId) {
        const tmp = path.join(os.tmpdir(), `horizon_db_${Date.now()}.json`);
        registerTemp(tmp);
        try {
            await this.downloadFile(fileId, tmp, null, 0);
            return JSON.parse(fs.readFileSync(tmp, 'utf8'));
        } finally {
            try { fs.unlinkSync(tmp); } catch (_) {}
            unregisterTemp(tmp);
        }
    }

    async deleteFile(fileId) { await this._call('DELETE', `/v1.0/me/drive/items/${fileId}`); }

    async getQuota() {
        const res = await this._call('GET', '/v1.0/me/drive?$select=quota');
        const q   = res.body.quota || {};
        return {
            used     : q.used      || 0,
            total    : q.total     || 0,
            remaining: q.remaining || 0,
        };
    }
}

module.exports = { OneDriveProvider };