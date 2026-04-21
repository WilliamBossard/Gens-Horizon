/**
 * index.js
 * Point d'entrée CLI de Gens Horizon.
 */

'use strict';

const fs = require('fs');
const path = require('path');
const { exec } = require('child_process');
const http = require('http');
const url = require('url');

const SETTINGS_PATH = path.join(process.cwd(), 'horizon_settings.json');

if (!fs.existsSync(SETTINGS_PATH)) {
    const defaultSettings = { systemEnabled: true, syncMode: 'SMART', autoSync: true, autoUpload: true, provider: 'google' };
    fs.writeFileSync(SETTINGS_PATH, JSON.stringify(defaultSettings, null, 2));
}

const args = process.argv.slice(2);

const getActiveProviderName = () => {
    const pArg = args.find(a => a.startsWith('--provider='))?.split('=')[1];
    if (pArg) return pArg;
    try {
        const settings = JSON.parse(fs.readFileSync(SETTINGS_PATH, 'utf8'));
        return settings.provider || 'google';
    } catch(e) { return 'google'; }
};

const providerName = getActiveProviderName();

const providerMap = {
    google:   require('./providers/google'),
    dropbox:  require('./providers/dropbox'),
    onedrive: require('./providers/onedrive')
};

const providerObj = providerMap[providerName];
const config = require('./config');

if (args.includes('--login')) {
    try {
        if (!providerObj) {
            throw new Error(`Fournisseur inconnu : ${providerName}`);
        }

        const providerConfig = config.credentials[providerName];
        if (!providerConfig) {
            throw new Error(`Configuration manquante dans config.js pour : ${providerName}`);
        }

        const redirectUri = providerConfig.redirect_uri;
        const PORT = parseInt(new URL(redirectUri).port) || 80;

        const server = http.createServer(async (req, res) => {
            try {
                const query = url.parse(req.url, true).query;
                if (query.code) {
                    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
                    res.end("<h1>Authentification réussie !</h1><p>Vous pouvez fermer cet onglet.</p>");
                    
                    await providerObj.handleAuthCode(query.code);
                    console.log(JSON.stringify({ type: "SUCCESS", message: "Jeton sauvegardé avec succès !" }));
                    
                    setTimeout(() => { server.close(); process.exit(0); }, 1000);
                }
            } catch (err) {
                console.log(JSON.stringify({ type: "ERROR", message: "Erreur lors du traitement du code : " + err.message }));
            }
        });

        server.on('error', (e) => {
            console.log(JSON.stringify({ type: "ERROR", message: e.code === 'EADDRINUSE' ? `Port ${PORT} déjà utilisé.` : e.message }));
            process.exit(1);
        });

        server.listen(PORT, () => {
            console.log(JSON.stringify({ type: "INFO", message: `Serveur Horizon prêt sur le port ${PORT}` }));
            
            try {
                const authUrl = providerObj.getAuthUrl();
                
                let command = '';
                if (process.platform === 'win32') {
                    command = `start "" "${authUrl}"`;
                } else if (process.platform === 'darwin') {
                    command = `open "${authUrl}"`;
                } else {
                    command = `xdg-open "${authUrl}"`;
                }

                exec(command, (err) => {
                    if (err) {
                        console.log(JSON.stringify({ type: "LOG", message: "Ouverture auto échouée. Lien manuel ci-dessous." }));
                        console.log(JSON.stringify({ type: "LOG", message: `URL : ${authUrl}` }));
                    }
                });

            } catch (err) {
                console.log(JSON.stringify({ type: "ERROR", message: "Erreur génération URL : " + err.message }));
                process.exit(1);
            }
        });
        setTimeout(() => {
            console.log(JSON.stringify({ type: "ERROR", message: "Délai d'attente dépassé." }));
            process.exit(1);
        }, 180000);

    } catch (globalErr) {
        console.log(JSON.stringify({ type: "ERROR", message: globalErr.message }));
        process.exit(1);
    }

} else if (args.includes('--check')) {
    require('./check.js');
} else if (args.includes('--sync')) {
    require('./sync.js');
} else if (args.includes('--upload')) {
    require('./upload.js');
} else {
    console.log(JSON.stringify({ type: "ERROR", message: "Commande manquante." }));
}