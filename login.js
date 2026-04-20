const fs = require('fs');
const http = require('http');
const https = require('https');
const url = require('url');
const { google } = require('googleapis');
const { exec } = require('child_process');
const path = require('path');

const TOKEN_PATH = path.join(process.cwd(), 'token.json');
const { credentials } = require('./config');

const AUTH_TIMEOUT_MS = 5 * 60 * 1000;

function openBrowser(targetUrl) {
    let command = "";
    
    if (process.platform === 'win32') {
        command = `start "" "${targetUrl}"`;
    } else if (process.platform === 'darwin') {
        command = `open "${targetUrl}"`;
    } else {
        command = `xdg-open "${targetUrl}"`;
    }

    exec(command, (error) => {
        if (error) {
            console.log(JSON.stringify({ 
                type: "ERROR", 
                message: "Impossible d'ouvrir le navigateur. Copie l'URL manuellement." 
            }));
        }
    });
}

async function loginPlayer() {
    const key = credentials.web;
    const REDIRECT_URI = 'http://127.0.0.1:12543';

    const oauth2Client = new google.auth.OAuth2(
        key.client_id,
        key.client_secret,
        REDIRECT_URI
    );

    const authUrl = oauth2Client.generateAuthUrl({
        access_type: 'offline',
        prompt: 'consent',
        scope: ['https://www.googleapis.com/auth/drive.appdata']
    });

    return new Promise((resolve, reject) => {
        let settled = false;

        const server = http.createServer(async (req, res) => {
            const parsedUrl = new url.URL(req.url, REDIRECT_URI);

            if (parsedUrl.searchParams.has('error')) {
                const errorCode = parsedUrl.searchParams.get('error');
                res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                res.end('<h1 style="color:red; text-align:center; font-family:sans-serif;">❌ Connexion annulée.<br>Tu peux fermer cette page.</h1>');
                
                if (!settled) {
                    settled = true;
                    clearTimeout(authTimeout);
                    setTimeout(() => {
                        server.close();
                        reject(new Error(`Connexion Google refusée : ${errorCode}`));
                    }, 800);
                }
                return;
            }

            if (!parsedUrl.searchParams.has('code')) return;
            const code = parsedUrl.searchParams.get('code');

            const postData = new URLSearchParams({
                code,
                client_id:     key.client_id,
                client_secret: key.client_secret,
                redirect_uri:  REDIRECT_URI,
                grant_type:    'authorization_code'
            }).toString();

            const options = {
                hostname: 'oauth2.googleapis.com',
                port: 443,
                path: '/token',
                method: 'POST',
                headers: {
                    'Content-Type': 'application/x-www-form-urlencoded',
                    'Content-Length': Buffer.byteLength(postData)
                }
            };

            const postReq = https.request(options, (postRes) => {
                let body = '';
                postRes.on('data', (chunk) => body += chunk);
                postRes.on('end', () => {
                    try {
                        const tokens = JSON.parse(body);
                        if (tokens.error) throw new Error(tokens.error_description || tokens.error);

                        fs.writeFileSync(TOKEN_PATH, JSON.stringify(tokens, null, 2));

                        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                        res.end('<h1 style="color:green; text-align:center; font-family:sans-serif;">✅ Connexion réussie !<br>Tu peux fermer cette page.</h1>');

                        if (!settled) {
                            settled = true;
                            clearTimeout(authTimeout);
                            setTimeout(() => {
                                server.close();
                                resolve(tokens);
                                process.exit(0);
                            }, 1000);
                        }
                    } catch (e) {
                        res.writeHead(500);
                        res.end("Erreur lors de l'échange du token.");
                        if (!settled) {
                            settled = true;
                            clearTimeout(authTimeout);
                            server.close();
                            reject(e);
                        }
                    }
                });
            });

            postReq.on('error', (e) => {
                if (!settled) {
                    settled = true;
                    clearTimeout(authTimeout);
                    server.close();
                    reject(e);
                }
            });
            postReq.write(postData);
            postReq.end();
        });

        const authTimeout = setTimeout(() => {
            if (!settled) {
                settled = true;
                server.close();
                reject(new Error(`Délai d'authentification dépassé (${AUTH_TIMEOUT_MS / 60000} minutes).`));
                process.exit(1);
            }
        }, AUTH_TIMEOUT_MS);

server.listen(12543, '127.0.0.1', () => {
    console.log(JSON.stringify({ type: "INFO", message: "Serveur Horizon prêt sur le port 12543. Ouverture du navigateur..." }));
    openBrowser(authUrl);
});

        server.on('error', (e) => {
            clearTimeout(authTimeout);
            reject(new Error("Impossible de démarrer le serveur local : " + e.message));
        });
    });
}

loginPlayer()
    .then(() => console.log(JSON.stringify({ type: "SUCCESS", message: "Jeton sauvegardé avec succès." })))
    .catch(err => {
        console.log(JSON.stringify({ type: "ERROR", message: err.message }));
        process.exit(1);
    });