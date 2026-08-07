'use strict';
const fs = require('fs');
const yauzl = require('yauzl');
const path = require('path');

// Wrapper Promise pour yauzl.open
function openZip(zipPath, options = {}) {
    return new Promise((resolve, reject) => {
        yauzl.open(zipPath, options, (err, zipfile) => {
            if (err) reject(err);
            else resolve(zipfile);
        });
    });
}

function openZipStream(zipfile, entry) {
    return new Promise((resolve, reject) => {
        zipfile.openReadStream(entry, (err, readStream) => {
            if (err) reject(err);
            else resolve(readStream);
        });
    });
}

// AUDIT-22 : new Promise(async ...) antipattern remplace par async function pure
async function verifyZipIntegrity(zipPath) {
    let fd;
    try {
        fd = await fs.promises.open(zipPath, 'r');
        const buf = Buffer.alloc(4);
        await fd.read(buf, 0, 4, 0);
        await fd.close();
        fd = null;
        if (buf[0] !== 0x50 || buf[1] !== 0x4B || buf[2] !== 0x03 || buf[3] !== 0x04) {
            const hex = buf.toString('hex').toUpperCase();
            throw new Error(`Fichier telecharge invalide (pas un ZIP). Signature recue: 0x${hex}.`);
        }
        // Validation avec yauzl (verifie le Central Directory)
        try {
            const zipfile = await openZip(zipPath, { lazyEntries: true });
            zipfile.close(); // si ça s'ouvre sans erreur, le central directory est valide
        } catch (openErr) {
            throw new Error(`Le fichier ZIP est tronque ou invalide : ${openErr.message}`);
        }
    } catch (e) {
        if (fd) { try { await fd.close(); } catch (_) {} }
        throw new Error(`Impossible de lire le fichier telecharge : ${e.message}`);
    }
}

async function extractZip(zipPath, targetPath, onProgress) {
    let zipfile;
    try {
        zipfile = await openZip(zipPath, { lazyEntries: true });
    } catch (err) {
        throw new Error(`Extraction impossible (Archive corrompue) : ${err.message}`);
    }

    const resolvedTarget = path.resolve(targetPath);
    const total = zipfile.entryCount;
    let count = 0;
    const limit = 20;
    const active = new Set();
    let errs = [];

    return new Promise((resolve, reject) => {
        zipfile.on('error', err => {
            errs.push(err);
            reject(err);
        });

        zipfile.on('end', async () => {
            await Promise.all(active);
            if (errs.length > 0) reject(errs[0]);
            else resolve();
        });

        zipfile.on('entry', async (entry) => {
            if (errs.length > 0) return; // Arret
            
            const dest = path.join(targetPath, entry.fileName);
            const resDest = path.resolve(dest);
            if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                zipfile.readEntry(); // Skip
                return;
            }

            try {
                if (/\/$/.test(entry.fileName)) { // Dossier
                    await fs.promises.mkdir(dest, { recursive: true });
                    zipfile.readEntry();
                } else {
                    await fs.promises.mkdir(path.dirname(dest), { recursive: true });
                    const readStream = await openZipStream(zipfile, entry);
                    
                    const p = new Promise((res, rej) => {
                        readStream.pipe(fs.createWriteStream(dest))
                            .on('finish', res)
                            .on('error', rej);
                    }).catch(e => { errs.push(e); });
                    
                    active.add(p);
                    p.then(() => active.delete(p));
                    
                    count++;
                    if (onProgress && count % 50 === 0) {
                        const fakePct = Math.min(99, Math.floor((count / total) * 100));
                        onProgress(fakePct);
                    }

                    if (active.size >= limit) {
                        await Promise.race(active);
                    }
                    zipfile.readEntry(); // Passe a la suivante
                }
            } catch (e) {
                errs.push(e);
                reject(e);
            }
        });

        zipfile.readEntry();
    });
}


async function applyDelta(deltaZipPath, targetPath, onProgress) {
    let zipfile;
    try {
        zipfile = await openZip(deltaZipPath, { lazyEntries: true });
    } catch (err) {
        throw new Error(`Application delta impossible (Archive corrompue) : ${err.message}`);
    }

    const resolvedTarget = path.resolve(targetPath);
    let deletedFiles = [];
    const limit = 20;
    const active = new Set();
    let errs = [];

    return new Promise((resolve, reject) => {
        zipfile.on('error', err => {
            errs.push(err);
            reject(err);
        });

        zipfile.on('end', async () => {
            await Promise.all(active);
            if (errs.length > 0) return reject(errs[0]);
            
            // Suppression differee
            for (const relPath of deletedFiles) {
                const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
                if (!path.resolve(absPath).startsWith(resolvedTarget + path.sep)) continue;
                try { if (await fs.promises.access(absPath).then(()=>true).catch(()=>false)) await fs.promises.rm(absPath, { recursive: true, force: true }); } catch (_) { }
            }
            resolve();
        });

        zipfile.on('entry', async (entry) => {
            if (errs.length > 0) return;
            
            if (entry.fileName === '__delta__.json') {
                try {
                    const readStream = await openZipStream(zipfile, entry);
                    let data = '';
                    readStream.on('data', chunk => data += chunk);
                    readStream.on('end', () => {
                        try { deletedFiles = JSON.parse(data).deletedFiles || []; } catch (_) { }
                        zipfile.readEntry();
                    });
                } catch (e) {
                    errs.push(e); reject(e);
                }
                return;
            }

            const dest = path.join(targetPath, entry.fileName);
            const resDest = path.resolve(dest);
            if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                zipfile.readEntry();
                return;
            }

            try {
                if (/\/$/.test(entry.fileName)) {
                    await fs.promises.mkdir(dest, { recursive: true });
                    zipfile.readEntry();
                } else {
                    await fs.promises.mkdir(path.dirname(dest), { recursive: true });
                    const readStream = await openZipStream(zipfile, entry);
                    
                    const p = new Promise((res, rej) => {
                        readStream.pipe(fs.createWriteStream(dest))
                            .on('finish', res)
                            .on('error', rej);
                    }).catch(e => { errs.push(e); });
                    
                    active.add(p);
                    p.then(() => active.delete(p));
                    
                    if (active.size >= limit) {
                        await Promise.race(active);
                    }
                    zipfile.readEntry();
                }
            } catch (e) {
                errs.push(e);
                reject(e);
            }
        });

        zipfile.readEntry();
    });
}

module.exports = {
    verifyZipIntegrity, extractZip, applyDelta
};
