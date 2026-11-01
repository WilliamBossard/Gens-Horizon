'use strict';
/**
 * GENS HORIZON — Configuration OAuth2
 * 
 * Ce fichier sert de modèle pour configurer vos identifiants d'API Cloud.
 * Pour le développement local, copiez ce fichier sous le nom `config.js`
 * et renseignez vos client_id et client_secret.
 */
const config = {
    credentials: {
        google: {
            client_id: "VOTRE_GOOGLE_CLIENT_ID",
            client_secret: "VOTRE_GOOGLE_CLIENT_SECRET",
            auth_uri: "https://accounts.google.com/o/oauth2/auth",
            token_uri: "https://oauth2.googleapis.com/token",
            redirect_uri: "http://127.0.0.1:12543"
        },
        dropbox: {
            client_id: "VOTRE_DROPBOX_CLIENT_ID",
            client_secret: "VOTRE_DROPBOX_CLIENT_SECRET",
            auth_uri: "https://www.dropbox.com/oauth2/authorize",
            token_uri: "https://api.dropbox.com/oauth2/token",
            redirect_uri: "http://127.0.0.1:12544"
        },
        onedrive: {
            client_id: "VOTRE_MICROSOFT_CLIENT_ID",
            client_secret: "",
            auth_uri: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
            token_uri: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
            redirect_uri: "http://127.0.0.1:12545",
            scope: "Files.ReadWrite offline_access User.Read"
        }
    }
};

module.exports = {
    getConfig: (provider) => {
        const provConfig = config.credentials[provider];
        if (!provConfig) throw new Error(`Fournisseur inconnu : ${provider}`);
        if (!provConfig.client_id || provConfig.client_id.startsWith("VOTRE_")) {
            throw new Error(`Configuration OAuth2 manquante ou incomplète pour ${provider}. Veuillez éditer config.js.`);
        }
        return provConfig;
    },
    ...config
};

