# Gens-Horizon — Guide de Développement Complet

Ce document décrit en profondeur l'architecture, le modèle de synchronisation, les primitives cryptographiques et les protocoles de communication de **Gens-Horizon**, le moteur Cloud "headless" (sans interface graphique) de l'écosystème Gens.

---

## 1. Vue d'Ensemble & Architecture Globale
Gens-Horizon est un moteur CLI autonome écrit en Node.js, pensé pour s'exécuter en arrière-plan. Il est chargé de la synchronisation bidirectionnelle des instances de jeu avec des fournisseurs Cloud (Google Drive, Dropbox, OneDrive).

Il gère le versionnement des fichiers via une approche de **"Delta Sync"** (synchronisation différentielle) et d'**"Incremental Backups"**, garantissant une vitesse fulgurante et une économie drastique de la bande passante et du stockage.

### Composants Principaux
- **`index.js`** : Routeur CLI principal. Gère les commandes passées au binaire (ex: `--sync`, `--login`, `--rollback`). Il redirige les flux `console.log` vers `stderr` pour protéger le flux JSON IPC.
- **`provider.js` & `/providers`** : Implémente le pattern Factory (`getProvider()`). Ce design rend le moteur agnostique au Cloud utilisé. Chaque fournisseur hérite d'une interface commune gérant l'authentification OAuth2, les quotas, et le téléchargement/envoi de fichiers.
- **`sync.js` / `upload.js`** : Les cœurs du moteur de synchronisation.
  - `upload.js` identifie les fichiers modifiés, crée une archive partielle (Delta), et l'envoie sur le Cloud.
  - `sync.js` interroge le Cloud, télécharge les Deltas manquants, et les applique dans l'ordre chronologique localement.
- **`scanner.js`** : Le scanner de fichiers local. Il utilise le hachage **SHA-256** couplé à des vérifications rapides (mtime/size) et un limiteur de concurrence (`withConcurrency`) pour générer instantanément un Manifeste d'instance sans saturer le disque (EMFILE).
- **`rollback.js`** : Permet de restaurer une instance locale à l'état exact d'un Delta passé, reconstituant l'historique de manière déterministe.
- **`zip-utils.js`** : Utilitaires pour l'extraction sécurisée de fichiers ZIP.
  - Protection stricte contre les attaques par **Path Traversal** (`resDest.startsWith(resolvedTarget)`).
  - Vérification de la signature magique ZIP (`0x504B0304`) ET de la table centrale avant extraction.
  - Chemin d'extraction principal (`unzipper.Open.file`) avec fallback streaming unifié (`_extractViaStream`) pour les ZIPs dont la table centrale est illisible.

---

## 2. Protocole de Communication (IPC JSON)
Le moteur Horizon s'exécute comme un processus enfant du Launcher. Il communique de manière asynchrone via les flux d'entrées/sorties standards (`stdout` et `stdin`).

Toutes les données émises sur `stdout` sont rigoureusement formatées en JSON pur. Exemple :
`json
{
  "type": "PROGRESS",
  "step": "COMPRESSING",
  "value": 45,
  "instance": "Aventure"
}
`
*Note : Tous les `console.log/warn` émis par les librairies internes sont interceptés et envoyés vers `process.stderr` pour éviter la corruption du parsing JSON côté Launcher.*

Les types de messages courants incluent : `PROGRESS`, `INFO`, `SUCCESS`, `ERROR`, `ROLLBACK_LIST`, `CHECK_RESULT`, `CLOUD_LIST`.

---

## 3. Sécurité Cryptographique & Verrous (Security Design)

L'application suit les recommandations NIST et applique la "Défense en Profondeur" :

1. **Authentification Hardware-Bound (`Auth.js`)** :
   Les jetons OAuth2 ne sont jamais stockés en clair. Ils sont chiffrés sur le disque en `AES-256-GCM` (le mode authentifié GCM remplace l'ancien CBC pour une sécurité maximale).
   La clé de chiffrement (32 octets) est dérivée de la manière suivante :
   - Fonction : `PBKDF2`
   - Itérations : **600 000** (Standard NIST/OWASP actuel pour bloquer le brute-forcing hors-ligne).
   - Sel : Fichier `salt.key` aléatoire (16 bytes) généré au premier lancement, stocké avec `mode: 0o600`.
   - Mot de passe : Contenu de `.machine_id` — un identifiant aléatoire de **256 bits** (32 bytes) généré une seule fois au premier lancement avec `crypto.randomBytes(32)`. Contrairement au hostname, cet ID ne peut pas être deviné ou reproduit sur une autre machine.
   *Le vol du dossier Horizon sans la machine physique (et son `.machine_id`) rend le déchiffrement virtuellement impossible.*

   Migration transparente : Si un token est chiffré avec l'ancien format (AES-CBC), il est automatiquement migré vers AES-GCM au premier déchiffrement réussi.

2. **Intégrité de Synchronisation (`lock.js`)** :
   Un système de verrous (lock file) empêche le lancement de multiples opérations de synchronisation simultanées (qui corrompraient l'instance).
   - Mécanisme : Le fichier `horizon.lock` contient le PID du processus maître. La création est atomique (`O_CREAT | O_EXCL`).
   - **Heartbeat** : Le timestamp du lock est mis à jour toutes les 5 secondes (`utimesSync`) pour distinguer les processus actifs des processus zombies.
   - "Stale Lock" : Un lock est considéré comme périmé si le processus PID n'est plus en cours d'exécution (`ESRCH`) ou s'il date de plus de 2 heures (`STALE_LOCK_MS = 7200000`). Il est alors purgé automatiquement. Si la suppression échoue (erreur OS), Horizon abandonne proprement sans busy-wait.

3. **Protection CI/CD (`config.js`)** :
   Les identifiants OAuth (Client ID / Secret) ne sont pas hardcodés. Si les identifiants injectés à la compilation sont manquants (évalués à "fake"), le moteur refuse de démarrer pour éviter toute fuite.

---

## 4. Tests et Qualité

Le projet inclut une couverture de test via le module natif `node:test`.
- Pour exécuter les tests localement : `npm test`
- Les tests vérifient l'intégrité de la cryptographie symétrique, le système de retry adaptatif, les verrous, et le hachage avec limiteur de concurrence.
- L'Intégration Continue (GitHub Actions) exécute les tests sur les environnements Linux et Windows avant toute compilation native via `pkg`.
