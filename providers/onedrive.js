'use strict';

const fs    = require('fs');
const https = require('https');
const path  = require('path');
const { credentials } = require('../config');
const Auth = require('../Auth'); 

const TOKEN_PATH = path.join(process.cwd(), 'token_onedrive.json');
const GRAPH_HOST = 'graph.microsoft.com';
const APP_ROOT   = '/v1.0/me/drive/special/approot';
const creds = credentials.onedrive;

function graphRequest(method, path, accessToken, body = null) {
    const bodyBuf = body ? Buffer.from(JSON.stringify(body)) : null;
    return new Promise((resolve, reject) => {
        const headers = { 'Authorization': `Bearer ${accessToken}`, 'Accept': 'application/json' };
        if (bodyBuf) { headers['Content-Type'] = 'application/json'; headers['Content-Length'] = bodyBuf.length; }
        const req = https.request({ hostname: GRAPH_HOST, path, method, headers }, (res) => {
            const chunks = [];
            res.on('data', c => chunks.push(c));
            res.on('end', () => {
                const raw = Buffer.concat(chunks).toString('utf8');
                resolve({ statusCode: res.statusCode, body: raw ? JSON.parse(raw) : {} });
            });
        });
        req.on('error', reject);
        if (bodyBuf) req.write(bodyBuf);
        req.end();
    });
}

function getAuthUrl() {
    const scopes = ['files.readwrite', 'offline_access', 'User.Read'].join(' ');
    return `https://login.microsoftonline.com/common/oauth2/v2.0/authorize?client_id=${creds.client_id}&scope=${encodeURIComponent(scopes)}&response_type=code&redirect_uri=${encodeURIComponent(creds.redirect_uri)}`;
}

async function handleAuthCode(code) {
    const params = new URLSearchParams({
        client_id: creds.client_id,
        client_secret: creds.client_secret,
        code: code,
        redirect_uri: creds.redirect_uri,
        grant_type: 'authorization_code'
    });
    const buf = Buffer.from(params.toString());
    const res = await new Promise((resolve, reject) => {
        const req = https.request({
            hostname: 'login.microsoftonline.com', path: '/common/oauth2/v2.0/token', method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Content-Length': buf.length }
        }, (r) => {
            const chunks = [];
            r.on('data', c => chunks.push(c));
            r.on('end', () => resolve({ statusCode: r.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }));
        });
        req.on('error', reject); req.write(buf); req.end();
    });

    if (res.statusCode !== 200) throw new Error('OneDrive Token Error');

    Auth.encryptToken(TOKEN_PATH, res.body);

    return res.body;
}

class OneDriveProvider {
    constructor(tokenData, credentials) {
        this._token   = tokenData.access_token;
        this._refresh = tokenData.refresh_token;
        this._creds   = credentials;
    }

    async _refreshToken() {
        const params = new URLSearchParams({ grant_type: 'refresh_token', refresh_token: this._refresh, client_id: this._creds.client_id, scope: 'Files.ReadWrite offline_access' });
        if (this._creds.client_secret) params.set('client_secret', this._creds.client_secret);
        const buf = Buffer.from(params.toString());
        const res = await new Promise((resolve, reject) => {
            const req = https.request({ hostname: 'login.microsoftonline.com', path: '/common/oauth2/v2.0/token', method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Content-Length': buf.length } }, (r) => {
                const chunks = []; r.on('data', c => chunks.push(c));
                r.on('end', () => { try { resolve({ statusCode: r.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }); } catch(e) { reject(e); } });
            });
            req.on('error', reject);
            req.write(buf); req.end();
        });
        if (!res.body.access_token) throw new Error('OneDrive token refresh failed: ' + (res.body.error_description || res.body.error || res.statusCode));
        this._token = res.body.access_token;
        if (res.body.refresh_token) this._refresh = res.body.refresh_token;
        Auth.encryptToken(TOKEN_PATH, {
            access_token : this._token,
            refresh_token: this._refresh,
            token_type   : res.body.token_type || 'Bearer'
        });
    }

    async _call(method, path, body = null) {
        let res = await graphRequest(method, path, this._token, body);
        if (res.statusCode === 401) { await this._refreshToken(); res = await graphRequest(method, path, this._token, body); }
        return res;
    }

    async listFiles(nameContains = '') {
        const res = await this._call('GET', `${APP_ROOT}/children?$top=1000`);
        return (res.body.value || [])
            .filter(i => i.file)
            .filter(i => !nameContains || i.name.includes(nameContains))
            .map(i => ({ id: i.id, name: i.name, modifiedTime: i.lastModifiedDateTime, size: i.size }));
    }

    async _uploadSession(name, srcPath, onProgress) {
        const CHUNK = 10 * 1024 * 1024; 
        const total = fs.statSync(srcPath).size;
        let offset  = 0;
        let lastPct = -1;
        let result  = null;

        const sessionRes = await this._call('POST', `${APP_ROOT}:/${encodeURIComponent(name)}:/createUploadSession`, { item: { '@microsoft.graph.conflictBehavior': 'replace' } });
        if (sessionRes.statusCode >= 400 || !sessionRes.body.uploadUrl) throw new Error(`OneDrive upload session failed`);
        const uploadUrl = new URL(sessionRes.body.uploadUrl);
        const fd = fs.openSync(srcPath, 'r');

        try {
            while (offset < total) {
                const end = Math.min(offset + CHUNK, total);
                const bytesToRead = end - offset;
                const buffer = Buffer.alloc(bytesToRead);
                fs.readSync(fd, buffer, 0, bytesToRead, offset); 
                const isLast = end === total;

                const res = await new Promise((resolve, reject) => {
                    const req = https.request({
                        hostname: uploadUrl.hostname, path: uploadUrl.pathname + uploadUrl.search, method: 'PUT',
                        headers: { 'Content-Length': bytesToRead, 'Content-Range': `bytes ${offset}-${end - 1}/${total}`, 'Content-Type': 'application/octet-stream' }
                    }, (r) => {
                        const chunks = []; r.on('data', c => chunks.push(c));
                        r.on('end', () => resolve({ statusCode: r.statusCode, body: chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {} }));
                    });
                    req.on('error', reject); req.write(buffer); req.end();
                });

                if (res.statusCode >= 400) throw new Error(`OneDrive chunk error ${res.statusCode}`);

                offset = end;
                if (onProgress && total > 0) {
                    const pct = isLast ? 100 : Math.min(99, Math.round(offset / total * 100));
                    if (pct !== lastPct) { onProgress(pct); lastPct = pct; }
                }
                if (isLast && (res.statusCode === 200 || res.statusCode === 201)) result = { id: res.body.id, modifiedTime: res.body.lastModifiedDateTime };
            }
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
                headers: { 'Authorization': `Bearer ${this._token}`, 'Content-Type': 'application/octet-stream', 'Content-Length': content.length }
            }, (r) => {
                const chunks = []; r.on('data', c => chunks.push(c));
                r.on('end', () => resolve({ statusCode: r.statusCode, body: chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {} }));
            });
            req.on('error', reject); req.write(content); req.end();
        });
        let res = await _doUpload();
        if (res.statusCode === 401) { await this._refreshToken(); res = await _doUpload(); }
        onProgress && onProgress(100);
        return { id: res.body.id, modifiedTime: res.body.lastModifiedDateTime };
    }

    async uploadJSON(name, content, existingId = null) {
        const buf = Buffer.from(JSON.stringify(content, null, 2));
        const _doUpload = async () => new Promise((resolve, reject) => {
            const req = https.request({
                hostname: GRAPH_HOST, path: `${APP_ROOT}:/${encodeURIComponent(name)}:/content`, method: 'PUT',
                headers: { 'Authorization': `Bearer ${this._token}`, 'Content-Type': 'application/json', 'Content-Length': buf.length }
            }, (r) => {
                const chunks = []; r.on('data', c => chunks.push(c));
                r.on('end', () => { try { resolve({ statusCode: r.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }); } catch(e) { reject(e); } });
            });
            req.on('error', reject); req.write(buf); req.end();
        });
        let res = await _doUpload();
        if (res.statusCode === 401) { await this._refreshToken(); res = await _doUpload(); }
        if (res.statusCode >= 400) throw new Error(`OneDrive uploadJSON error ${res.statusCode}: ${res.body?.message || ''}`);
        return { id: res.body.id, modifiedTime: res.body.lastModifiedDateTime };
    }

    async downloadFile(fileId, destPath, onProgress, totalSize) {
        const redirectUrl = await new Promise((resolve, reject) => {
            const req = https.request({
                hostname: GRAPH_HOST,
                path    : `/v1.0/me/drive/items/${fileId}/content`,
                method  : 'GET',
                headers : { 'Authorization': `Bearer ${this._token}` }
            }, (res) => {
                res.resume(); 
                if (res.statusCode === 302 || res.statusCode === 303) {
                    resolve(res.headers.location);
                } else if (res.statusCode === 401) {
                    reject(Object.assign(new Error('Unauthorized'), { statusCode: 401 }));
                } else {
                    reject(new Error(`OneDrive download: statut inattendu ${res.statusCode}`));
                }
            });
            req.on('error', reject);
            req.end();
        }).catch(async (e) => {
            if (e.statusCode === 401) {
                await this._refreshToken();
                return new Promise((resolve, reject) => {
                    const req = https.request({
                        hostname: GRAPH_HOST,
                        path    : `/v1.0/me/drive/items/${fileId}/content`,
                        method  : 'GET',
                        headers : { 'Authorization': `Bearer ${this._token}` }
                    }, (res) => {
                        res.resume();
                        if (res.statusCode === 302 || res.statusCode === 303) resolve(res.headers.location);
                        else reject(new Error(`OneDrive download: statut inattendu ${res.statusCode}`));
                    });
                    req.on('error', reject);
                    req.end();
                });
            }
            throw e;
        });

        const loc = new URL(redirectUrl);
        await new Promise((resolve, reject) => {
            const dest = fs.createWriteStream(destPath);
            let downloaded = 0, lastPct = -1;
            const req = https.request({ hostname: loc.hostname, path: loc.pathname + loc.search, method: 'GET' }, (res) => {
                res.on('data', chunk => {
                    downloaded += chunk.length;
                    if (totalSize > 0 && onProgress) {
                        const pct = Math.min(100, Math.round(downloaded / totalSize * 100));
                        if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) { onProgress(pct); lastPct = pct; }
                    }
                    dest.write(chunk);
                });
                res.on('end',   () => dest.end(resolve));
                res.on('error', e  => { dest.destroy(); reject(e); });
            });
            req.on('error', e => { dest.destroy(); reject(e); });
            req.end();
        });
    }

    async deleteFile(fileId) { await this._call('DELETE', `/v1.0/me/drive/items/${fileId}`); }
}

module.exports = { OneDriveProvider, getAuthUrl, handleAuthCode };