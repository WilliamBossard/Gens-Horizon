'use strict';

const fs = require('fs');
const http = require('http');
const https = require('https');
const url = require('url');
const path = require('path');
const { execFile, execSync } = require('child_process');

const crypto = require('crypto');
const { credentials } = require('./config');
const { getProviderName, getTokenPath } = require('./provider');
const { getHorizonDataDir } = require('./paths');
const { setupProcessHandlers } = require('./utils');

setupProcessHandlers();

const AUTH_TIMEOUT_MS = 5 * 60 * 1000;

function createOAuthState() {
    return crypto.randomBytes(16).toString('hex');
}

function openBrowser(targetUrl) {
    console.log(JSON.stringify({ type: 'AUTH_URL', message: targetUrl }));

    let cmd;
    if (process.platform === 'win32') {
        execFile('cmd', ['/c', 'start', '', targetUrl], () => { });
    } else if (process.platform === 'darwin') {
        execFile('open', [targetUrl], () => { });
    } else {
        try {
            execSync('which xdg-open', { stdio: 'ignore' });
            execFile('xdg-open', [targetUrl], () => { });
        } catch (_) {
            console.log(JSON.stringify({ type: 'INFO', message: 'Environnement headless détecté. Ouvre l\'URL manuellement dans un navigateur.' }));
            return;
        }
    }
}

function httpsPost(hostname, path, body) {
    return new Promise((resolve, reject) => {
        const buf = Buffer.from(body);
        const req = https.request({
            hostname, path, method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Content-Length': buf.length }
        }, (res) => {
            const chunks = [];
            res.on('data', c => chunks.push(c));
            res.on('end', () => {
                try { resolve(JSON.parse(Buffer.concat(chunks).toString())); }
                catch (e) { reject(e); }
            });
        });
        req.setTimeout(15000, () => { req.destroy(new Error('OAuth token exchange timeout')); });
        req.on('error', reject);
        req.write(buf);
        req.end();
    });
}

function waitForCallback(exchangeCode, port, expectedState) {
    return new Promise((resolve, reject) => {
        let settled = false;

        const server = http.createServer(async (req, res) => {
            const parsed = new url.URL(req.url, `http://127.0.0.1:${port}`);

            if (parsed.searchParams.has('error')) {
                res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end('<h1 style="color:red;text-align:center;font-family:sans-serif;">❌ Connexion annulée. Tu peux fermer cette page.</h1>');
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); reject(new Error('Connexion annulée.')); }, 600); }
                return;
            }
            if (!parsed.searchParams.has('code')) return;

            const returnedState = parsed.searchParams.get('state');
            if (!returnedState || returnedState !== expectedState) {
                res.writeHead(400, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end('<h1 style="color:red;text-align:center;font-family:sans-serif;">❌ State OAuth invalide.</h1>');
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); reject(new Error('State OAuth invalide (CSRF ?).')); }, 600); }
                return;
            }

            const code = parsed.searchParams.get('code');
            try {
                const tokens = await exchangeCode(code);
                res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end('<h1 style="color:green;text-align:center;font-family:sans-serif;">✅ Connexion réussie ! Tu peux fermer cette page.</h1>');
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); resolve(tokens); }, 800); }
            } catch (e) {
                res.writeHead(500);
                res.end('Erreur lors de l\'échange du token.');
                if (!settled) { settled = true; clearTimeout(timer); server.close(); reject(e); }
            }
        });

        const timer = setTimeout(() => {
            if (!settled) { settled = true; server.close(); reject(new Error('Délai d\'authentification dépassé.')); }
        }, AUTH_TIMEOUT_MS);

        server.listen(port, '127.0.0.1', () => {
            console.log(JSON.stringify({ type: 'INFO', message: `Serveur Horizon prêt (port ${port}). Ouverture du navigateur...` }));
        });
        server.on('error', e => { clearTimeout(timer); reject(new Error('Impossible de démarrer le serveur : ' + e.message)); });
    });
}

async function loginGoogle() {
    const cred = credentials.google;
    const REDIRECT_URI = cred.redirect_uri;
    const port = parseInt(new URL(REDIRECT_URI).port) || 80;
    const oauthState = createOAuthState();
    const authUrl = `${cred.auth_uri}?${new URLSearchParams({
        client_id: cred.client_id,
        redirect_uri: REDIRECT_URI,
        response_type: 'code',
        access_type: 'offline',
        prompt: 'consent',
        scope: 'https://www.googleapis.com/auth/drive.appdata',
        state: oauthState,
    }).toString()}`;

    openBrowser(authUrl);
    return waitForCallback(async (code) => {
        const tokens = await httpsPost('oauth2.googleapis.com', '/token', new URLSearchParams({
            code, client_id: cred.client_id, client_secret: cred.client_secret,
            redirect_uri: REDIRECT_URI, grant_type: 'authorization_code'
        }).toString());
        if (tokens.error) throw new Error(tokens.error_description || tokens.error);
        return tokens;
    }, port, oauthState);
}

async function loginDropbox() {
    const cred = credentials.dropbox;
    const REDIRECT_URI = cred.redirect_uri;
    const port = parseInt(new URL(REDIRECT_URI).port) || 80;
    const DROPBOX_AUTH_URL = 'https://www.dropbox.com/oauth2/authorize';
    const oauthState = createOAuthState();

    openBrowser(`${DROPBOX_AUTH_URL}?response_type=code&client_id=${cred.client_id}&redirect_uri=${encodeURIComponent(REDIRECT_URI)}&token_access_type=offline&state=${oauthState}`);
    return waitForCallback(async (code) => {
        const tokens = await httpsPost('api.dropbox.com', '/oauth2/token', new URLSearchParams({
            code, client_id: cred.client_id, client_secret: cred.client_secret,
            redirect_uri: REDIRECT_URI, grant_type: 'authorization_code'
        }).toString());
        if (tokens.error) throw new Error(tokens.error_description || tokens.error);
        return tokens;
    }, port, oauthState);
}

async function loginOneDrive() {
    const cred = credentials.onedrive;
    const REDIRECT_URI = cred.redirect_uri;
    const port = parseInt(new URL(REDIRECT_URI).port) || 80;
    const usePKCE = !cred.client_secret;

    let verifier, challenge;
    if (usePKCE) {
        const crypto = require('crypto');
        verifier = crypto.randomBytes(32).toString('base64url');
        challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
    }

    const oauthState = createOAuthState();
    const params = new URLSearchParams({
        client_id: cred.client_id, response_type: 'code', redirect_uri: REDIRECT_URI,
        scope: cred.scope, prompt: 'select_account', state: oauthState,
        ...(usePKCE && { code_challenge: challenge, code_challenge_method: 'S256' })
    });

    openBrowser(`${cred.auth_uri}?${params.toString()}`);
    return waitForCallback(async (code) => {
        const tokens = await httpsPost('login.microsoftonline.com', '/common/oauth2/v2.0/token',
            new URLSearchParams({
                code, client_id: cred.client_id, redirect_uri: REDIRECT_URI,
                grant_type: 'authorization_code', scope: cred.scope,
                ...(usePKCE ? { code_verifier: verifier } : { client_secret: cred.client_secret })
            }).toString());
        if (tokens.error) throw new Error(tokens.error_description || tokens.error);
        return tokens;
    }, port, oauthState);
}

async function loginPlayer() {
    const settingsPath = path.join(getHorizonDataDir(), 'horizon_settings.json');
    const settings = (() => {
        try { return fs.existsSync(settingsPath) ? JSON.parse(fs.readFileSync(settingsPath, 'utf8')) : {}; } catch { return {}; }
    })();

    const providerName = getProviderName(settings);
    console.log(JSON.stringify({ type: 'INFO', message: `Connexion via ${providerName}...` }));

    let tokens;
    switch (providerName) {
        case 'google': tokens = await loginGoogle(); break;
        case 'dropbox': tokens = await loginDropbox(); break;
        case 'onedrive': tokens = await loginOneDrive(); break;
        default: throw new Error(`Fournisseur inconnu : ${providerName}`);
    }

    const { encryptToken } = require('./Auth');
    const tokenPath = getTokenPath(providerName);
    encryptToken(tokenPath, tokens);

    if (fs.existsSync(settingsPath)) {
        try {
            const sets = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
            if (!sets.provider || sets.provider !== providerName) {
                sets.provider = providerName;
                fs.writeFileSync(settingsPath, JSON.stringify(sets, null, 2));
            }
        } catch (_) { }
    }
}

loginPlayer()
    .then(() => { console.log(JSON.stringify({ type: 'SUCCESS', message: 'Jeton sauvegardé avec succès.' })); process.exit(0); })
    .catch(err => { console.log(JSON.stringify({ type: 'ERROR', message: err.message })); process.exit(1); });