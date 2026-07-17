'use strict';
const fs = require('fs');
const unzipper = require('unzipper');
const path = require('path');

function verifyZipIntegrity(zipPath) {
    return new Promise(async (resolve, reject) => {
        try {
            const fd = fs.openSync(zipPath, 'r');
            const buf = Buffer.alloc(4);
            fs.readSync(fd, buf, 0, 4, 0);
            fs.closeSync(fd);
            if (buf[0] !== 0x50 || buf[1] !== 0x4B || buf[2] !== 0x03 || buf[3] !== 0x04) {
                const hex = buf.toString('hex').toUpperCase();
                return reject(new Error(`Fichier téléchargé invalide (pas un ZIP). Signature reçue: 0x${hex}.`));
            }

            // Validation avancée de la table centrale (détecte si le fichier est tronqué)
            try {
                const directory = await unzipper.Open.file(zipPath);
                if (!directory || !directory.files) {
                    return reject(new Error(`Le fichier ZIP est corrompu ou incomplet.`));
                }
            } catch (openErr) {
                return reject(new Error(`Le fichier ZIP est tronqué ou invalide : ${openErr.message}`));
            }

            resolve();
        } catch (e) {
            reject(new Error(`Impossible de lire le fichier téléchargé : ${e.message}`));
        }
    });
}


async function extractZip(zipPath, targetPath, onProgress) {
    let directory;
    try {
        directory = await unzipper.Open.file(zipPath);
    } catch (err) {
        return new Promise((resolve, reject) => {
            const resolvedTarget = path.resolve(targetPath);
            let count = 0;
            let activeWrites = 0;
            let zipFinished = false;
            const checkFinish = () => { if (zipFinished && activeWrites === 0) resolve(); };
                const finishFn = () => { zipFinished = true; checkFinish(); };
                fs.createReadStream(zipPath)
                    .pipe(unzipper.Parse())
                    .on('entry', function (entry) {
                        const dest = path.join(targetPath, entry.path);
                        const resDest = path.resolve(dest);
                        if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                            entry.autodrain();
                            return;
                        }
                        if (entry.type === 'Directory' || /[\/\\]$/.test(entry.path)) {
                            fs.mkdirSync(dest, { recursive: true });
                            entry.autodrain();
                        } else {
                            fs.mkdirSync(path.dirname(dest), { recursive: true });
                            const ws = fs.createWriteStream(dest);
                            activeWrites++;
                            ws.on('finish', () => { activeWrites--; checkFinish(); });
                            ws.on('error', (err) => { activeWrites--; reject(err); });
                            entry.pipe(ws);
                        }
                        count++;
                    })
                    .on('close', finishFn)
                    .on('end', finishFn)
                    .on('finish', finishFn)
                    .on('error', (err) => { reject(err); });
        });
    }

    const resolvedTarget = path.resolve(targetPath);
    let count = 0;
    const total = directory.files.length;
    const limit = 20;
    const active = new Set();
    let errs = [];

    for (const file of directory.files) {
        if (errs.length > 0) break;
        const dest = path.join(targetPath, file.path);
        const resDest = path.resolve(dest);
        if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
            continue;
        }
        if (file.type === 'Directory' || /[\/\\]$/.test(file.path)) {
            fs.mkdirSync(dest, { recursive: true });
        } else {
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            const p = new Promise((resolve, reject) => {
                file.stream()
                    .pipe(fs.createWriteStream(dest))
                    .on('finish', resolve)
                    .on('error', reject);
            }).catch(e => { errs.push(e); });
            active.add(p);
            p.then(() => active.delete(p));
            if (active.size >= limit) await Promise.race(active);
        }
        count++;
        if (onProgress && count % 50 === 0) {
            const fakePct = Math.min(99, Math.floor((count / total) * 100));
            onProgress(fakePct);
        }
    }
    await Promise.all(active);
    if (errs.length > 0) throw errs[0];
}

async function applyDelta(deltaZipPath, targetPath, onProgress) {
    let directory;
    const resolvedTarget = path.resolve(targetPath);
    try {
        directory = await unzipper.Open.file(deltaZipPath);
    } catch (err) {
        return new Promise((resolve, reject) => {
            let deletedFiles = [];
            let activeWrites = 0;
            let zipFinished = false;
            const checkFinish = () => {
                if (zipFinished && activeWrites === 0) {
                    for (const relPath of deletedFiles) {
                        const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
                        if (!path.resolve(absPath).startsWith(resolvedTarget + path.sep)) continue;
                        try { if (fs.existsSync(absPath)) fs.rmSync(absPath, { recursive: true, force: true }); } catch (_) { }
                    }
                    resolve();
                }
            };
                const finishFn = () => { zipFinished = true; checkFinish(); };
                fs.createReadStream(deltaZipPath)
                    .pipe(unzipper.Parse())
                    .on('entry', function (entry) {
                        if (entry.path === '__delta__.json') {
                            let data = '';
                            entry.on('data', chunk => data += chunk);
                            entry.on('end', () => {
                                try { deletedFiles = JSON.parse(data).deletedFiles || []; } catch (_) { }
                            });
                            return;
                        }
                        const dest = path.join(targetPath, entry.path);
                        const resDest = path.resolve(dest);
                        if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                            entry.autodrain();
                            return;
                        }
                        if (entry.type === 'Directory' || /[\/\\]$/.test(entry.path)) {
                            fs.mkdirSync(dest, { recursive: true });
                            entry.autodrain();
                        } else {
                            fs.mkdirSync(path.dirname(dest), { recursive: true });
                            const ws = fs.createWriteStream(dest);
                            activeWrites++;
                            ws.on('finish', () => { activeWrites--; checkFinish(); });
                            ws.on('error', (err) => { activeWrites--; reject(err); });
                            entry.pipe(ws);
                        }
                    })
                    .on('close', finishFn)
                    .on('end', finishFn)
                    .on('finish', finishFn)
                    .on('error', (err) => { reject(err); });
        });
    }

    let deletedFiles = [];
    const deltaFile = directory.files.find(f => f.path === '__delta__.json');
    if (deltaFile) {
        const buf = await deltaFile.buffer();
        try { deletedFiles = JSON.parse(buf.toString()).deletedFiles || []; } catch (_) { }
    }

    const limit = 20;
    const active = new Set();
    let errs = [];

    for (const file of directory.files) {
        if (file.path === '__delta__.json') continue;
        if (errs.length > 0) break;
        const dest = path.join(targetPath, file.path);
        const resDest = path.resolve(dest);
        if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
            continue;
        }
        if (file.type === 'Directory' || /[\/\\]$/.test(file.path)) {
            fs.mkdirSync(dest, { recursive: true });
        } else {
            fs.mkdirSync(path.dirname(dest), { recursive: true });
            const p = new Promise((resolve, reject) => {
                file.stream()
                    .pipe(fs.createWriteStream(dest))
                    .on('finish', resolve)
                    .on('error', reject);
            }).catch(e => { errs.push(e); });
            active.add(p);
            p.then(() => active.delete(p));
            if (active.size >= limit) await Promise.race(active);
        }
    }
    await Promise.all(active);
    if (errs.length > 0) throw errs[0];

    for (const relPath of deletedFiles) {
        const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
        if (!path.resolve(absPath).startsWith(resolvedTarget + path.sep)) continue;
        try { if (fs.existsSync(absPath)) fs.rmSync(absPath, { recursive: true, force: true }); } catch (_) { }
    }
}

module.exports = {
    verifyZipIntegrity, extractZip, applyDelta
};
