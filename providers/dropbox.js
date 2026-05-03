'use strict';

const fs    = require('fs');
const https = require('https');
const path  = require('path');
const os    = require('os');
const { credentials } = require('../config');
const Auth = require('../Auth'); 

const TOKEN_PATH = path.join(process.cwd(), 'token_dropbox.json');
const creds = credentials.dropbox;

function httpsRequest(options, bodyBuffer = null) {
    return new Promise((resolve, reject) => {
        const req = https.request(options, (res) => {
            const chunks = [];
            res.on('data', c => chunks.push(c));
            res.on('end',  () => {
                const raw  = Buffer.concat(chunks).toString('utf8');
                const body = (() => { try { return JSON.parse(raw); } catch { return raw; } })();
                resolve({ statusCode: res.statusCode, headers: res.headers, body });
            });
        });
        req.on('error', reject);
        if (bodyBuffer) req.write(bodyBuffer);
        req.end();
    });
}

function httpsDownload(options, destPath, onProgress, totalSize) {
    return new Promise((resolve, reject) => {
        const dest = fs.createWriteStream(destPath);
        let downloaded = 0, lastPct = -1;

        const req = https.request(options, (res) => {
            if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
                dest.destroy();
                const loc = new URL(res.headers.location);
                httpsDownload({
                    hostname: loc.hostname,
                    path    : loc.pathname + loc.search,
                    method  : 'GET'
                }, destPath, onProgress, totalSize).then(resolve).catch(reject);
                return;
            }

            if (res.statusCode < 200 || res.statusCode >= 300) {
                const errChunks = [];
                res.on('data', c => errChunks.push(c));
                res.on('end', () => {
                    dest.destroy();
                    try { fs.unlinkSync(destPath); } catch(_) {}
                    const errBody = Buffer.concat(errChunks).toString('utf8').slice(0, 300);
                    const err = Object.assign(
                        new Error(`Dropbox download HTTP ${res.statusCode}: ${errBody}`),
                        { statusCode: res.statusCode }
                    );
                    reject(err);
                });
                return;
            }

            res.on('data', chunk => {
                downloaded += chunk.length;
                if (totalSize > 0) {
                    const pct = Math.min(100, Math.round(downloaded / totalSize * 100));
                    if (pct !== lastPct && (pct >= lastPct + 2 || pct === 100)) {
                        onProgress && onProgress(pct);
                        lastPct = pct;
                    }
                }
                dest.write(chunk);
            });
            res.on('end',   () => dest.end(() => resolve()));
            res.on('error', e  => { dest.destroy(); reject(e); });
        });
        req.on('error', e => { dest.destroy(); reject(e); });
        req.end();
    });
}

function getAuthUrl() {
    return `https://www.dropbox.com/oauth2/authorize?client_id=${creds.client_id}&response_type=code&redirect_uri=${encodeURIComponent(creds.redirect_uri)}&token_access_type=offline`;
}

async function handleAuthCode(code) {
    const params = new URLSearchParams({
        code,
        grant_type: 'authorization_code',
        client_id: creds.client_id,
        client_secret: creds.client_secret,
        redirect_uri: creds.redirect_uri
    });
    const buf = Buffer.from(params.toString());
    const res = await httpsRequest({
        hostname: 'api.dropbox.com',
        path: '/oauth2/token',
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Content-Length': buf.length }
    }, buf);

    if (res.statusCode !== 200) throw new Error('Dropbox Token Error');

    Auth.encryptToken(TOKEN_PATH, res.body);
    
    return res.body;
}

class DropboxProvider {
    constructor(tokenData, credentials) {
        this._token  = tokenData.access_token;
        this._refresh = tokenData.refresh_token;
        this._creds  = credentials;
    }

    _authHeader() {
        return `Bearer ${this._token}`;
    }

    async _api(endpoint, body) {
        const bodyBuf = Buffer.from(JSON.stringify(body));
        const res = await httpsRequest({
            hostname: 'api.dropboxapi.com',
            path    : `/2/${endpoint}`,
            method  : 'POST',
            headers : {
                'Authorization' : this._authHeader(),
                'Content-Type'  : 'application/json',
                'Content-Length': bodyBuf.length
            }
        }, bodyBuf);

        if (res.statusCode === 401) {
            await this._refreshToken();
            return this._api(endpoint, body);
        }
        return res.body;
    }

    async _refreshToken() {
        const params = new URLSearchParams({
            grant_type   : 'refresh_token',
            refresh_token: this._refresh,
            client_id    : this._creds.client_id,
            client_secret: this._creds.client_secret
        });
        const buf = Buffer.from(params.toString());
        const res = await httpsRequest({
            hostname: 'api.dropbox.com',
            path    : '/oauth2/token',
            method  : 'POST',
            headers : { 'Content-Type': 'application/x-www-form-urlencoded', 'Content-Length': buf.length }
        }, buf);
        if (!res.body.access_token) throw new Error('Dropbox token refresh failed: ' + (res.body.error_description || res.body.error || res.statusCode));
        this._token = res.body.access_token;
        Auth.encryptToken(TOKEN_PATH, {
            access_token : this._token,
            refresh_token: this._refresh,
            token_type   : res.body.token_type || 'bearer'
        });
    }

    async listFiles(nameContains = '') {
        const res = await this._api('files/list_folder', { path: '', recursive: false });
        let entries = res.entries || [];
        return entries
            .filter(e => e['.tag'] === 'file')
            .filter(e => !nameContains || e.name.includes(nameContains))
            .map(e => ({
                id          : e.id,
                name        : e.name,
                modifiedTime: e.server_modified,
                size        : e.size
            }));
    }

    async _uploadSession(name, srcPath, onProgress) {
        const CHUNK = 8 * 1024 * 1024; 
        const total = fs.statSync(srcPath).size;
        let offset  = 0;
        let lastPct = -1;
        
        const fd = fs.openSync(srcPath, 'r');

        const _report = (off) => {
            if (!onProgress || total === 0) return;
            const pct = Math.min(99, Math.round(off / total * 100));
            if (pct !== lastPct) { onProgress(pct); lastPct = pct; }
        };

        try {
            let bytesToRead = Math.min(CHUNK, total);
            let buffer = Buffer.alloc(bytesToRead);
            fs.readSync(fd, buffer, 0, bytesToRead, offset);
            
            const startRes = await httpsRequest({
                hostname: 'content.dropboxapi.com', path: '/2/files/upload_session/start', method: 'POST',
                headers: { 'Authorization': this._authHeader(), 'Dropbox-API-Arg': JSON.stringify({ close: false }), 'Content-Type': 'application/octet-stream', 'Content-Length': bytesToRead }
            }, buffer);
            const sessionId = startRes.body.session_id;
            offset += bytesToRead;
            _report(offset);

            while (offset + CHUNK < total) {
                bytesToRead = Math.min(CHUNK, total - offset);
                buffer = Buffer.alloc(bytesToRead);
                fs.readSync(fd, buffer, 0, bytesToRead, offset);

                await httpsRequest({
                    hostname: 'content.dropboxapi.com', path: '/2/files/upload_session/append_v2', method: 'POST',
                    headers: { 'Authorization': this._authHeader(), 'Dropbox-API-Arg': JSON.stringify({ cursor: { session_id: sessionId, offset }, close: false }), 'Content-Type': 'application/octet-stream', 'Content-Length': bytesToRead }
                }, buffer);
                offset += bytesToRead;
                _report(offset);
            }

            const lastBytes = total - offset;
            buffer = Buffer.alloc(lastBytes);
            if (lastBytes > 0) fs.readSync(fd, buffer, 0, lastBytes, offset);

            const finishRes = await httpsRequest({
                hostname: 'content.dropboxapi.com', path: '/2/files/upload_session/finish', method: 'POST',
                headers: { 'Authorization': this._authHeader(), 'Dropbox-API-Arg': JSON.stringify({ cursor: { session_id: sessionId, offset }, commit: { path: `/${name}`, mode: 'overwrite', autorename: false } }), 'Content-Type': 'application/octet-stream', 'Content-Length': lastBytes }
            }, buffer);
            
            onProgress && onProgress(100);
            return { id: finishRes.body.id, modifiedTime: finishRes.body.server_modified };
        } finally {
            fs.closeSync(fd);  
        }
    }

    async uploadZip(name, srcPath, existingId = null, onProgress = null) {
        return this._uploadSession(name, srcPath, onProgress);
    }

    async downloadFile(fileId, destPath, onProgress, totalSize) {
        const _doDownload = (authHeader) => httpsDownload({
            hostname: 'content.dropboxapi.com',
            path    : '/2/files/download',
            method  : 'POST',
            headers : { 'Authorization': authHeader, 'Dropbox-API-Arg': JSON.stringify({ path: fileId }), 'Content-Type': '' }
        }, destPath, onProgress, totalSize);

        try {
            await _doDownload(this._authHeader());
        } catch (e) {
            if (e.statusCode === 401 || (e.message && e.message.includes('401'))) {
                await this._refreshToken();
                await _doDownload(this._authHeader());
            } else { throw e; }
        }
    }

    async uploadJSON(name, content) {
        const buf = Buffer.from(JSON.stringify(content, null, 2));
        const arg = JSON.stringify({ path: `/${name}`, mode: 'overwrite' });
        const _doUpload = (authHeader) => httpsRequest({
            hostname: 'content.dropboxapi.com',
            path: '/2/files/upload',
            method: 'POST',
            headers: { 'Authorization': authHeader, 'Dropbox-API-Arg': arg, 'Content-Type': 'application/octet-stream', 'Content-Length': buf.length }
        }, buf);
        let res = await _doUpload(this._authHeader());
        if (res.statusCode === 401) { await this._refreshToken(); res = await _doUpload(this._authHeader()); }
        if (res.statusCode >= 400) throw new Error(`Dropbox uploadJSON error ${res.statusCode}: ${res.body?.error_summary || ''}`);
        return { id: res.body.id, modifiedTime: res.body.server_modified };
    }

    async downloadJSON(fileId) {
        return new Promise((resolve, reject) => {
            const chunks = [];
            const options = {
                hostname: 'content.dropboxapi.com',
                path    : '/2/files/download',
                method  : 'POST',
                headers : {
                    'Authorization'  : this._authHeader(),
                    'Dropbox-API-Arg': JSON.stringify({ path: fileId }),
                    'Content-Type'   : ''
                }
            };
            const req = https.request(options, (res) => {
                if (res.statusCode === 401) {
                    res.resume();
                    reject(Object.assign(new Error('Unauthorized'), { statusCode: 401 }));
                    return;
                }
                res.on('data', c => chunks.push(c));
                res.on('end', () => {
                    try { resolve(JSON.parse(Buffer.concat(chunks).toString('utf8'))); }
                    catch (e) { reject(e); }
                });
                res.on('error', reject);
            });
            req.on('error', reject);
            req.end();
        }).catch(async (e) => {
            if (e.statusCode === 401) {
                await this._refreshToken();
                return this.downloadJSON(fileId);
            }
            throw e;
        });
    }

    async deleteFile(fileId) {
        await this._api('files/delete_v2', { path: fileId });
    }
}

module.exports = { DropboxProvider, getAuthUrl, handleAuthCode };
