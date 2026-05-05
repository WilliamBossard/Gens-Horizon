'use strict';

const fs    = require('fs');
const http  = require('http');
const https = require('https');
const url   = require('url');
const path  = require('path');
const { exec, execSync } = require('child_process');

const { credentials }    = require('./config');
const { getProviderName, getTokenPath } = require('./provider');

const AUTH_TIMEOUT_MS = 5 * 60 * 1000;
const CWD = process.cwd();

function openBrowser(targetUrl) {
    console.log(JSON.stringify({ type: 'AUTH_URL', message: targetUrl }));

    let cmd;
    if (process.platform === 'win32') {
        cmd = `start "" "${targetUrl}"`;
    } else if (process.platform === 'darwin') {
        cmd = `open "${targetUrl}"`;
    } else {
        try {
            execSync('which xdg-open', { stdio: 'ignore' });
            cmd = `xdg-open "${targetUrl}"`;
        } catch (_) {
            console.log(JSON.stringify({ type: 'INFO', message: 'Environnement headless détecté. Ouvre l\'URL manuellement dans un navigateur.' }));
            return;
        }
    }
    exec(cmd, () => {});
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
        req.on('error', reject);
        req.write(buf);
        req.end();
    });
}

function waitForCallback(exchangeCode, port) {
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
            if (!settled) { settled = true; server.close(); reject(new Error('Délai d\'authentification dépassé.')); process.exit(1); }
        }, AUTH_TIMEOUT_MS);

        server.listen(port, '127.0.0.1', () => {
            console.log(JSON.stringify({ type: 'INFO', message: `Serveur Horizon prêt (port ${port}). Ouverture du navigateur...` }));
        });
        server.on('error', e => { clearTimeout(timer); reject(new Error('Impossible de démarrer le serveur : ' + e.message)); });
    });
}

async function loginGoogle() {
    const cred         = credentials.google;
    const REDIRECT_URI = cred.redirect_uri;
    const port         = parseInt(new URL(REDIRECT_URI).port) || 80;
    const { google }   = require('googleapis');

    const oauth2 = new google.auth.OAuth2(cred.client_id, cred.client_secret, REDIRECT_URI);
    const authUrl = oauth2.generateAuthUrl({ access_type: 'offline', prompt: 'consent', scope: ['https://www.googleapis.com/auth/drive.appdata'] });

    openBrowser(authUrl);
    return waitForCallback(async (code) => {
        const tokens = await httpsPost('oauth2.googleapis.com', '/token', new URLSearchParams({
            code, client_id: cred.client_id, client_secret: cred.client_secret,
            redirect_uri: REDIRECT_URI, grant_type: 'authorization_code'
        }).toString());
        if (tokens.error) throw new Error(tokens.error_description || tokens.error);
        return tokens;
    }, port);
}

async function loginDropbox() {
    const cred         = credentials.dropbox;
    const REDIRECT_URI = cred.redirect_uri;
    const port         = parseInt(new URL(REDIRECT_URI).port) || 80;

    openBrowser(`${cred.auth_uri}?response_type=code&client_id=${cred.client_id}&redirect_uri=${encodeURIComponent(REDIRECT_URI)}&token_access_type=offline`);
    return waitForCallback(async (code) => {
        const tokens = await httpsPost('api.dropbox.com', '/oauth2/token', new URLSearchParams({
            code, client_id: cred.client_id, client_secret: cred.client_secret,
            redirect_uri: REDIRECT_URI, grant_type: 'authorization_code'
        }).toString());
        if (tokens.error) throw new Error(tokens.error_description || tokens.error);
        return tokens;
    }, port);
}

async function loginOneDrive() {
    const cred         = credentials.onedrive;
    const REDIRECT_URI = cred.redirect_uri;
    const port         = parseInt(new URL(REDIRECT_URI).port) || 80;
    const usePKCE      = !cred.client_secret;

    let verifier, challenge;
    if (usePKCE) {
        const crypto = require('crypto');
        verifier  = crypto.randomBytes(32).toString('base64url');
        challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
    }

    const params = new URLSearchParams({
        client_id: cred.client_id, response_type: 'code', redirect_uri: REDIRECT_URI,
        scope: cred.scope, prompt: 'select_account',
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
    }, port);
}

async function loginPlayer() {
    const settings = (() => {
        const p = path.join(CWD, 'horizon_settings.json');
        try { return fs.existsSync(p) ? JSON.parse(fs.readFileSync(p, 'utf8')) : {}; } catch { return {}; }
    })();

    const providerName = getProviderName(settings);
    console.log(JSON.stringify({ type: 'INFO', message: `Connexion via ${providerName}...` }));

    let tokens;
    switch (providerName) {
        case 'google'  : tokens = await loginGoogle();   break;
        case 'dropbox' : tokens = await loginDropbox();  break;
        case 'onedrive': tokens = await loginOneDrive(); break;
        default: throw new Error(`Provider inconnu : ${providerName}`);
    }

    const { encryptToken } = require('./Auth');
    const tokenPath = path.join(CWD, `token_${providerName}.json`);
    encryptToken(tokenPath, tokens);
    if (providerName === 'google') encryptToken(path.join(CWD, 'token.json'), tokens);

    const settingsFilePath = path.join(CWD, 'horizon_settings.json');
    if (fs.existsSync(settingsFilePath)) {
        try {
            const sets = JSON.parse(fs.readFileSync(settingsFilePath, 'utf8'));
            if (!sets.provider || sets.provider !== providerName) {
                sets.provider = providerName;
                fs.writeFileSync(settingsFilePath, JSON.stringify(sets, null, 2));
            }
        } catch (_) {}
    }
}

loginPlayer()
    .then(() => { console.log(JSON.stringify({ type: 'SUCCESS', message: 'Jeton sauvegardé avec succès.' })); process.exit(0); })
    .catch(err => { console.log(JSON.stringify({ type: 'ERROR', message: err.message })); process.exit(1); });
