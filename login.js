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
const { getProviderName, getHorizonDataDir, getAppDataPath } = require('./paths');
const { setupProcessHandlers, existsSafe } = require('./utils');
setupProcessHandlers();

const AUTH_TIMEOUT_MS = 5 * 60 * 1000;

function createOAuthState() {
    return crypto.randomBytes(16).toString('hex');
}

const I18N = {
    fr: {
        badgeSuccess: 'Gens-Horizon • Synchronisation Cloud',
        badgeError: 'Gens-Horizon • Authentification',
        successTitle: 'Connexion réussie !',
        successMsg: 'Votre compte Cloud a été authentifié et associé avec succès à Gens Launcher.',
        successSubtext: 'Tu peux fermer cet onglet et continuer à jouer.',
        cancelTitle: 'Connexion annulée',
        cancelMsg: 'La demande de connexion a été annulée ou refusée par le fournisseur.',
        cancelSubtext: 'Tu peux fermer cette page et relancer la connexion depuis le launcher.',
        csrfTitle: 'Erreur de sécurité',
        csrfMsg: 'Le jeton de sécurité de session (state OAuth) est invalide.',
        csrfSubtext: 'Tentative de requête potentiellement altérée. Veuillez réessayer.',
        errorTitle: 'Échec de connexion',
        errorSubtext: 'Veuillez vérifier vos identifiants et réessayer.',
        closeBtn: 'Fermer cet onglet',
        closeBlockedMsg: 'Votre navigateur bloque la fermeture automatique des onglets ouverts depuis une application externe.<br>Vous pouvez fermer cet onglet en toute sécurité avec <strong id="shortcut-key">Ctrl + W</strong>.',
        shortcutLabel: 'Raccourci : ',
        chip1: 'Hardware-Bound',
        chip2: 'AES-256-GCM',
        chip3: 'Gens Ecosystem'
    },
    en: {
        badgeSuccess: 'Gens-Horizon • Cloud Sync',
        badgeError: 'Gens-Horizon • Authentication',
        successTitle: 'Connection Successful!',
        successMsg: 'Your Cloud account has been authenticated and successfully linked to Gens Launcher.',
        successSubtext: 'You can now close this tab and continue playing.',
        cancelTitle: 'Connection Cancelled',
        cancelMsg: 'The connection request was cancelled or declined by the provider.',
        cancelSubtext: 'You can close this page and restart the connection from the launcher.',
        csrfTitle: 'Security Error',
        csrfMsg: 'The OAuth session security token (state) is invalid.',
        csrfSubtext: 'Potentially modified request detected. Please try again.',
        errorTitle: 'Connection Failed',
        errorSubtext: 'Please check your credentials and try again.',
        closeBtn: 'Close this tab',
        closeBlockedMsg: 'Your browser prevents scripts from automatically closing external tabs.<br>You can safely close this tab using <strong id="shortcut-key">Ctrl + W</strong>.',
        shortcutLabel: 'Shortcut: ',
        chip1: 'Hardware-Bound',
        chip2: 'AES-256-GCM',
        chip3: 'Gens Ecosystem'
    }
};

async function getLauncherLanguage() {
    // 1. Argument en ligne de commande (--lang=en ou --lang en)
    const cliLang = process.argv.find(a => a.startsWith('--lang='));
    if (cliLang) {
        const val = cliLang.split('=')[1].trim().toLowerCase();
        if (['en', 'fr'].includes(val)) return val;
    }
    const langIdx = process.argv.indexOf('--lang');
    if (langIdx !== -1 && process.argv[langIdx + 1]) {
        const val = process.argv[langIdx + 1].trim().toLowerCase();
        if (['en', 'fr'].includes(val)) return val;
    }
    // 2. Fichier settings.json de GensLauncher (%APPDATA%/GensLauncher/settings.json)
    const launcherSettingsPath = path.join(getAppDataPath(), 'GensLauncher', 'settings.json');
    if (await existsSafe(launcherSettingsPath)) {
        try {
            const parsed = JSON.parse(await fs.promises.readFile(launcherSettingsPath, 'utf8'));
            if (parsed.language && typeof parsed.language === 'string') {
                const l = parsed.language.trim().toLowerCase();
                if (l.startsWith('en')) return 'en';
                if (l.startsWith('fr')) return 'fr';
            }
        } catch (_) {}
    }
    // 3. Fichier horizon_settings.json
    const horizonSettingsPath = path.join(getHorizonDataDir(), 'horizon_settings.json');
    if (await existsSafe(horizonSettingsPath)) {
        try {
            const parsed = JSON.parse(await fs.promises.readFile(horizonSettingsPath, 'utf8'));
            const l = (parsed.language || parsed.lang || '').trim().toLowerCase();
            if (l.startsWith('en')) return 'en';
            if (l.startsWith('fr')) return 'fr';
        } catch (_) {}
    }
    return 'fr';
}

function renderAuthPage({ success, type = 'success', lang = 'fr', customTitle, customMessage, customSubtext }) {
    const isSuccess = Boolean(success);
    const isEn = lang === 'en';
    const t = isEn ? I18N.en : I18N.fr;

    const primaryColor = isSuccess ? '#10b981' : '#ef4444';
    const glowColor = isSuccess ? 'rgba(16, 185, 129, 0.2)' : 'rgba(239, 68, 68, 0.2)';
    const badgeText = isSuccess ? t.badgeSuccess : t.badgeError;
    const badgeColor = isSuccess ? '#38bdf8' : '#f87171';
    const badgeBg = isSuccess ? 'rgba(56, 189, 248, 0.12)' : 'rgba(239, 68, 68, 0.12)';
    const badgeBorder = isSuccess ? 'rgba(56, 189, 248, 0.3)' : 'rgba(239, 68, 68, 0.3)';

    let title = customTitle;
    let message = customMessage;
    let subtext = customSubtext;

    if (!title) {
        if (type === 'success') {
            title = t.successTitle;
            message = t.successMsg;
            subtext = t.successSubtext;
        } else if (type === 'cancel') {
            title = t.cancelTitle;
            message = t.cancelMsg;
            subtext = t.cancelSubtext;
        } else if (type === 'csrf') {
            title = t.csrfTitle;
            message = t.csrfMsg;
            subtext = t.csrfSubtext;
        } else {
            title = t.errorTitle;
            subtext = t.errorSubtext;
        }
    }

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
<html lang="${isEn ? 'en' : 'fr'}">
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
            position: relative;
            max-width: 490px;
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
        .lang-switch {
            position: absolute;
            top: 1rem;
            right: 1.2rem;
            font-size: 0.75rem;
            font-weight: 600;
            color: #64748b;
            display: flex;
            gap: 0.3rem;
            align-items: center;
        }
        .lang-btn {
            background: none;
            border: none;
            color: #64748b;
            cursor: pointer;
            font-weight: 700;
            padding: 0.15rem 0.3rem;
            border-radius: 4px;
            transition: all 0.15s ease;
        }
        .lang-btn:hover, .lang-btn.active {
            color: #38bdf8;
            background: rgba(56, 189, 248, 0.1);
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
        .actions {
            display: flex;
            flex-direction: column;
            gap: 0.6rem;
            align-items: center;
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
        .btn-hint {
            background: rgba(255, 255, 255, 0.08) !important;
            border-color: rgba(56, 189, 248, 0.5) !important;
            color: #38bdf8 !important;
            box-shadow: none !important;
        }
        .shortcut-pill {
            display: inline-flex;
            align-items: center;
            gap: 0.35rem;
            margin-top: 0.2rem;
            padding: 0.3rem 0.75rem;
            background: rgba(255, 255, 255, 0.04);
            border: 1px solid rgba(255, 255, 255, 0.08);
            border-radius: 6px;
            font-size: 0.78rem;
            color: #94a3b8;
        }
        .key-icon {
            display: inline-block;
            padding: 0.12rem 0.45rem;
            background: rgba(255, 255, 255, 0.1);
            border: 1px solid rgba(255, 255, 255, 0.2);
            border-radius: 4px;
            font-family: ui-monospace, monospace;
            font-weight: 700;
            font-size: 0.75rem;
            color: #f1f5f9;
        }
        .footer-note {
            margin-top: 1.25rem;
            font-size: 0.82rem;
            color: #64748b;
            line-height: 1.5;
        }
        .hint-alert {
            color: #38bdf8 !important;
            background: rgba(56, 189, 248, 0.08);
            border: 1px solid rgba(56, 189, 248, 0.2);
            border-radius: 8px;
            padding: 0.6rem 0.8rem;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="lang-switch">
            <button type="button" class="lang-btn ${!isEn ? 'active' : ''}" onclick="applyLang('fr')">FR</button>
            <span>/</span>
            <button type="button" class="lang-btn ${isEn ? 'active' : ''}" onclick="applyLang('en')">EN</button>
        </div>
        <div id="badge-el" class="badge">${badgeText}</div>
        <div class="icon-circle">
            ${iconSvg}
        </div>
        <h1 id="title-el">${title}</h1>
        <p id="msg-el" class="message">${message}</p>
        ${isSuccess ? `
        <div class="chips">
            <span class="chip">${t.chip1}</span>
            <span class="chip">${t.chip2}</span>
            <span class="chip">${t.chip3}</span>
        </div>` : ''}
        <div class="actions">
            <button id="close-btn" class="btn" onclick="tryCloseTab()">
                <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                    <line x1="18" y1="6" x2="6" y2="18"></line>
                    <line x1="6" y1="6" x2="18" y2="18"></line>
                </svg>
                <span id="btn-text">${t.closeBtn}</span>
            </button>
            <div id="shortcut-badge" class="shortcut-pill">
                <span id="shortcut-lbl">${t.shortcutLabel}</span>
                <span class="key-icon" id="key-modifier">Ctrl</span> + <span class="key-icon">W</span>
            </div>
        </div>
        <div id="close-hint" class="footer-note">${subtext || ''}</div>
    </div>
    <script>
        const DICT = ${JSON.stringify(I18N)};
        const pageType = ${JSON.stringify(type)};
        const isSuccessPage = ${isSuccess ? 'true' : 'false'};
        let currentLang = ${JSON.stringify(isEn ? 'en' : 'fr')};
        const isMac = navigator.platform.toUpperCase().indexOf('MAC') >= 0 || navigator.userAgent.toUpperCase().indexOf('MAC') >= 0;

        document.addEventListener('DOMContentLoaded', function() {
            const modKey = document.getElementById('key-modifier');
            if (modKey && isMac) {
                modKey.textContent = '⌘';
            }
        });

        function applyLang(lang) {
            currentLang = lang;
            const t = DICT[lang] || DICT.fr;
            const isEn = (lang === 'en');

            document.documentElement.lang = isEn ? 'en' : 'fr';
            document.querySelectorAll('.lang-btn').forEach(btn => {
                btn.classList.toggle('active', btn.textContent.trim().toLowerCase() === lang);
            });

            const badge = document.getElementById('badge-el');
            if (badge) badge.textContent = isSuccessPage ? t.badgeSuccess : t.badgeError;

            const titleEl = document.getElementById('title-el');
            const msgEl = document.getElementById('msg-el');
            const hintEl = document.getElementById('close-hint');

            if (pageType === 'success') {
                if (titleEl) titleEl.textContent = t.successTitle;
                if (msgEl) msgEl.textContent = t.successMsg;
                if (hintEl) hintEl.textContent = t.successSubtext;
            } else if (pageType === 'cancel') {
                if (titleEl) titleEl.textContent = t.cancelTitle;
                if (msgEl) msgEl.textContent = t.cancelMsg;
                if (hintEl) hintEl.textContent = t.cancelSubtext;
            } else if (pageType === 'csrf') {
                if (titleEl) titleEl.textContent = t.csrfTitle;
                if (msgEl) msgEl.textContent = t.csrfMsg;
                if (hintEl) hintEl.textContent = t.csrfSubtext;
            } else {
                if (titleEl) titleEl.textContent = t.errorTitle;
                if (hintEl) hintEl.textContent = t.errorSubtext;
            }

            const btnText = document.getElementById('btn-text');
            if (btnText) btnText.textContent = t.closeBtn;

            const shortcutLbl = document.getElementById('shortcut-lbl');
            if (shortcutLbl) shortcutLbl.textContent = t.shortcutLabel;
        }

        function tryCloseTab() {
            window.close();
            try {
                window.open('', '_self', '');
                window.close();
            } catch (_) {}

            setTimeout(function() {
                const btn = document.getElementById('close-btn');
                const btnText = document.getElementById('btn-text');
                const hint = document.getElementById('close-hint');
                const shortcutStr = isMac ? '⌘ + W' : 'Ctrl + W';
                const isEn = (currentLang === 'en');

                if (btn) btn.classList.add('btn-hint');
                if (btnText) {
                    btnText.textContent = isEn ? ('Press ' + shortcutStr + ' to close') : ('Appuyez sur ' + shortcutStr + ' pour fermer');
                }
                if (hint) {
                    hint.innerHTML = isEn
                        ? 'Your browser prevents scripts from closing external tabs.<br><strong>You can safely close this tab now using ' + shortcutStr + '.</strong>'
                        : 'Votre navigateur bloque la fermeture automatique des onglets ouverts depuis une application.<br><strong>Vous pouvez fermer cet onglet en toute sécurité avec ' + shortcutStr + '.</strong>';
                    hint.classList.add('hint-alert');
                }
            }, 150);
        }

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

function waitForCallback(exchangeCode, port, expectedState, lang = 'fr') {
    return new Promise((resolve, reject) => {
        let settled = false;
        const server = http.createServer(async (req, res) => {
            const parsed = new url.URL(req.url, `http://127.0.0.1:${port}`);
            let effectiveLang = lang;
            if (req.headers && req.headers['accept-language']) {
                const acc = req.headers['accept-language'].toLowerCase();
                if (!lang && (acc.startsWith('en') || (acc.includes('en') && !acc.includes('fr')))) {
                    effectiveLang = 'en';
                }
            }

            if (parsed.searchParams.has('error')) {
                res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end(renderAuthPage({
                    success: false,
                    type: 'cancel',
                    lang: effectiveLang
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
                    type: 'csrf',
                    lang: effectiveLang
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
                    type: 'success',
                    lang: effectiveLang
                }));
                if (!settled) { settled = true; clearTimeout(timer); setTimeout(() => { server.close(); resolve(tokens); }, 800); }
            } catch (e) {
                res.writeHead(500, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end(renderAuthPage({
                    success: false,
                    type: 'error',
                    lang: effectiveLang,
                    customMessage: e.message || 'Erreur lors de l\'échange du jeton.'
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

async function loginGoogle(lang) {
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
    }, port, oauthState, lang);
}

async function loginDropbox(lang) {
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
    }, port, oauthState, lang);
}

async function loginOneDrive(lang) {
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
    }, port, oauthState, lang);
}

async function loginPlayer() {
    const settingsPath = path.join(getHorizonDataDir(), 'horizon_settings.json');
    const settings = await (async () => {
        try { return await existsSafe(settingsPath) ? JSON.parse(await fs.promises.readFile(settingsPath, 'utf8')) : {}; } catch { return {}; }
    })();
    const providerName = getProviderName(settings);
    const lang = await getLauncherLanguage();
    console.log(JSON.stringify({ type: 'INFO', message: `Connexion via ${providerName}...` }));
    let tokens;
    switch (providerName) {
        case 'google': tokens = await loginGoogle(lang); break;
        case 'dropbox': tokens = await loginDropbox(lang); break;
        case 'onedrive': tokens = await loginOneDrive(lang); break;
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
