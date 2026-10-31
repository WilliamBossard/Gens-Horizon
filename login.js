'use strict';
const fs = require('fs');
const http = require('http');
const https = require('https');
const url = require('url');
const path = require('path');
const { execFile } = require('child_process');
const crypto = require('crypto');
const { getConfig } = require('./config');
const { getTokenPath } = require('./provider');
const { getProviderName } = require('./paths');
const { getHorizonDataDir } = require('./paths');
const { setupProcessHandlers, existsSafe } = require('./utils');
setupProcessHandlers();
const AUTH_TIMEOUT_MS = 5 * 60 * 1000;
function createOAuthState() {
    return crypto.randomBytes(16).toString('hex');
}

function renderAuthPage({ success, title, message, subtext }) {
    const isSuccess = Boolean(success);
    const primaryColor = isSuccess ? '#10b981' : '#ef4444';
    const glowColor = isSuccess ? 'rgba(16, 185, 129, 0.2)' : 'rgba(239, 68, 68, 0.2)';
    const badgeText = isSuccess ? 'Gens-Horizon • Synchronisation Cloud' : 'Gens-Horizon • Authentification';
    const badgeColor = isSuccess ? '#38bdf8' : '#f87171';
    const badgeBg = isSuccess ? 'rgba(56, 189, 248, 0.12)' : 'rgba(239, 68, 68, 0.12)';
    const badgeBorder = isSuccess ? 'rgba(56, 189, 248, 0.3)' : 'rgba(239, 68, 68, 0.3)';

    const iconSvg = isSuccess ? `
        <svg viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="status-svg">
            <path d="M20 6L9 17l-5-5"/>
        </svg>
    ` : `
        <svg viewBox="0 0 24 24" fill="none" stroke="#ef4444" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="status-svg">
            <line x1="18" y1="6" x2="6" y2="18"/>
            <line x1="6" y1="6" x2="18" y2="18"/>
        </svg>
    `;

    return `<!DOCTYPE html>
<html lang="fr">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>${title} — Gens Launcher</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
            background-color: #0b0f19;
            background-image: 
                radial-gradient(ellipse at 50% 0%, ${glowColor} 0%, transparent 65%),
                radial-gradient(ellipse at 80% 100%, rgba(14, 165, 233, 0.08) 0%, transparent 50%);
            color: #f1f5f9;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 1.5rem;
        }
        .container {
            max-width: 480px;
            width: 100%;
            background: linear-gradient(135deg, rgba(30, 41, 59, 0.75) 0%, rgba(15, 23, 42, 0.85) 100%);
            border: 1px solid rgba(56, 189, 248, 0.25);
            border-radius: 16px;
            padding: 2.5rem 2rem;
            text-align: center;
            backdrop-filter: blur(16px);
            -webkit-backdrop-filter: blur(16px);
            box-shadow: 0 20px 40px -10px rgba(0, 0, 0, 0.6), 0 0 50px -15px ${glowColor};
            animation: fadeIn 0.4s cubic-bezier(0.16, 1, 0.3, 1);
        }
        @keyframes fadeIn {
            from { opacity: 0; transform: translateY(12px) scale(0.98); }
            to { opacity: 1; transform: translateY(0) scale(1); }
        }
        .badge {
            display: inline-block;
            font-size: 0.72rem;
            font-weight: 700;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            color: ${badgeColor};
            background: ${badgeBg};
            border: 1px solid ${badgeBorder};
            padding: 0.3rem 0.8rem;
            border-radius: 9999px;
            margin-bottom: 1.5rem;
        }
        .icon-circle {
            width: 72px;
            height: 72px;
            margin: 0 auto 1.5rem;
            border-radius: 50%;
            background: ${glowColor};
            border: 2px solid ${primaryColor};
            display: flex;
            align-items: center;
            justify-content: center;
            box-shadow: 0 0 25px ${glowColor};
        }
        .status-svg {
            width: 38px;
            height: 38px;
        }
        h1 {
            font-size: 1.5rem;
            font-weight: 700;
            margin-bottom: 0.75rem;
            color: #ffffff;
            letter-spacing: -0.02em;
        }
        .message {
            color: #94a3b8;
            font-size: 0.95rem;
            line-height: 1.6;
            margin-bottom: 1.5rem;
        }
        .chips {
            display: flex;
            gap: 0.5rem;
            justify-content: center;
            flex-wrap: wrap;
            margin-bottom: 1.8rem;
        }
        .chip {
            font-size: 0.72rem;
            font-weight: 600;
            color: #cbd5e1;
            background: rgba(255, 255, 255, 0.06);
            border: 1px solid rgba(255, 255, 255, 0.1);
            padding: 0.25rem 0.6rem;
            border-radius: 6px;
            font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
        }
        .btn {
            display: inline-flex;
            align-items: center;
            justify-content: center;
            gap: 0.5rem;
            width: 100%;
            padding: 0.75rem 1.25rem;
            background: linear-gradient(135deg, #0284c7 0%, #0369a1 100%);
            border: 1px solid rgba(56, 189, 248, 0.4);
            color: #ffffff;
            font-size: 0.9rem;
            font-weight: 600;
            border-radius: 8px;
            cursor: pointer;
            text-decoration: none;
            transition: all 0.2s ease;
            box-shadow: 0 4px 15px rgba(2, 132, 199, 0.3);
        }
        .btn:hover {
            background: linear-gradient(135deg, #0369a1 0%, #075985 100%);
            transform: translateY(-1px);
            box-shadow: 0 6px 20px rgba(2, 132, 199, 0.4);
        }
        .footer-note {
            margin-top: 1.25rem;
            font-size: 0.8rem;
            color: #64748b;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="badge">${badgeText}</div>
        <div class="icon-circle">
            ${iconSvg}
        </div>
        <h1>${title}</h1>
        <p class="message">${message}</p>
        ${isSuccess ? `
        <div class="chips">
            <span class="chip">Hardware-Bound</span>
            <span class="chip">AES-256-GCM</span>
            <span class="chip">Gens Ecosystem</span>
        </div>` : ''}
        <button class="btn" onclick="window.close()">Fermer cet onglet</button>
        <div class="footer-note">${subtext || 'Tu peux retourner en toute sécurité sur Gens Launcher.'}</div>
    </div>
    <script>
        ${isSuccess ? 'setTimeout(function() { try { window.close(); } catch(e){} }, 5000);' : ''}
    </script>
</body>
</html>`;
}

function openBrowser(targetUrl) {
    console.log(JSON.stringify({ type: 'AUTH_URL', message: targetUrl }));
    if (process.platform === 'win32') {
        execFile('rundll32', ['url.dll,FileProtocolHandler', targetUrl], () => { });
    } else if (process.platform === 'darwin') {
        execFile('open', [targetUrl], () => { });
    } else {
        execFile('which', ['xdg-open'], (err) => {
            if (err) {
                console.log(JSON.stringify({ type: 'INFO', message: 'Environnement headless détecté. Ouvre l\'URL manuellement dans un navigateur.' }));
                return;
            }
            execFile('xdg-open', [targetUrl], () => { });
        });
    }
}
function httpsPost(hostname, reqPath, body) {
    return new Promise((resolve, reject) => {
        const buf = Buffer.from(body);
        const req = https.request({
            hostname, path: reqPath, method: 'POST',
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
                res.end(renderAuthPage({
                    success: false,
                    title: 'Connexion annulée',
                    message: 'La demande de connexion a été annulée ou refusée par le fournisseur.',
                    subtext: 'Tu peux fermer cette page et relancer la connexion depuis le launcher.'
                }));
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); reject(new Error('Connexion annulée.')); }, 600); }
                return;
            }
            if (!parsed.searchParams.has('code')) {
                res.writeHead(404);
                res.end();
                return;
            }
            const returnedState = parsed.searchParams.get('state');
            if (!returnedState || returnedState !== expectedState) {
                res.writeHead(400, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end(renderAuthPage({
                    success: false,
                    title: 'Erreur de sécurité',
                    message: 'Le jeton de sécurité de session (state OAuth) est invalide.',
                    subtext: 'Tentative de requête potentiellement altérée. Veuillez réessayer.'
                }));
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); reject(new Error('State OAuth invalide (CSRF ?).')); }, 600); }
                return;
            }
            const code = parsed.searchParams.get('code');
            try {
                const tokens = await exchangeCode(code);
                res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end(renderAuthPage({
                    success: true,
                    title: 'Connexion réussie !',
                    message: 'Votre compte Cloud a été authentifié et associé avec succès à Gens Launcher.',
                    subtext: 'Tu peux fermer cet onglet et continuer à jouer.'
                }));
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); resolve(tokens); }, 800); }
            } catch (e) {
                res.writeHead(500, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end(renderAuthPage({
                    success: false,
                    title: 'Échec de connexion',
                    message: e.message || 'Une erreur est survenue lors de l\'échange des clés avec le fournisseur.',
                    subtext: 'Veuillez vérifier vos identifiants et réessayer.'
                }));
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
    const cred = getConfig('google');
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
        if (tokens.expires_in && !tokens.expiry_date) {
            tokens.expiry_date = Date.now() + tokens.expires_in * 1000;
        }
        return tokens;
    }, port, oauthState);
}
async function loginDropbox() {
    const cred = getConfig('dropbox');
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
    const cred = getConfig('onedrive');
    const REDIRECT_URI = cred.redirect_uri;
    const port = parseInt(new URL(REDIRECT_URI).port) || 80;
    const usePKCE = !cred.client_secret;
    let verifier, challenge;
    if (usePKCE) {
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
        if (tokens.expires_in && !tokens.expiry_date) {
            tokens.expiry_date = Date.now() + tokens.expires_in * 1000;
        }
        return tokens;
    }, port, oauthState);
}
async function loginPlayer() {
    const settingsPath = path.join(getHorizonDataDir(), 'horizon_settings.json');
    const settings = await (async () => {
        try { return await existsSafe(settingsPath) ? JSON.parse(await fs.promises.readFile(settingsPath, 'utf8')) : {}; } catch { return {}; }
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
    const tokenPath = await getTokenPath(providerName);
    await encryptToken(tokenPath, tokens);
    if (await existsSafe(settingsPath)) {
        try {
            const sets = JSON.parse(await fs.promises.readFile(settingsPath, 'utf8'));
            if (sets.provider !== providerName) {
                sets.provider = providerName;
                await fs.promises.writeFile(settingsPath, JSON.stringify(sets, null, 2));
            }
        } catch (_) { if (_ && _.code !== 'ENOENT') console.error('[login.js] Erreur silencieuse interceptée:', _.message || _); }
    }
}
loginPlayer()
    .then(() => { console.log(JSON.stringify({ type: 'SUCCESS', message: 'Jeton sauvegardé avec succès.' })); process.exit(0); })
    .catch(err => { console.log(JSON.stringify({ type: 'ERROR', message: err.message })); process.exit(1); });
