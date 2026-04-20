const fs = require('fs');
const { google } = require('googleapis');
const path = require('path');

const TOKEN_PATH = path.join(process.cwd(), 'token.json');
async function getDriveClient() {
    const { credentials } = require('./config');
    const key = credentials.web;

    const oauth2Client = new google.auth.OAuth2(key.client_id, key.client_secret);
    oauth2Client.setCredentials(JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf8')));

    return google.drive({ version: 'v3', auth: oauth2Client });
}

async function testDrive() {
    const drive = await getDriveClient();

    try {
        const res = await drive.files.list({ spaces: 'appDataFolder' });
        console.log('✅ Connexion réussie ! Fichiers trouvés :', res.data.files.length);
    } catch (err) {
        console.error('❌ Erreur de connexion :', err.message);
    }
}

testDrive();