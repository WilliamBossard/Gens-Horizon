'use strict';
const fs = require('fs');
const yauzl = require('yauzl');
const path = require('path');
const { withConcurrency, safeRm } = require('./utils');

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
        if (fd) { try { await fd.close(); } catch (err) { if (err.code !== 'EBADF') console.error("Close fd failed", err); } }
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
    const entries = [];

    await new Promise((resolve, reject) => {
        zipfile.on('error', reject);
        zipfile.on('end', resolve);
        zipfile.on('entry', entry => {
            entries.push(entry);
            zipfile.readEntry();
        });
        zipfile.readEntry();
    }).catch(e => {
        try { zipfile.close(); } catch (_) {}
        throw e;
    });

    try {
        const tasks = entries.map(entry => async () => {
            const dest = path.join(targetPath, entry.fileName);
            const resDest = path.resolve(dest);
            if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) {
                return;
            }

            if (/\/$/.test(entry.fileName)) {
                await fs.promises.mkdir(dest, { recursive: true });
            } else {
                await fs.promises.mkdir(path.dirname(dest), { recursive: true });
                const readStream = await openZipStream(zipfile, entry);
                const { pipeline } = require('stream/promises');
                await pipeline(readStream, fs.createWriteStream(dest));
                count++;
                if (onProgress && count % 50 === 0) {
                    const fakePct = Math.min(99, Math.floor((count / total) * 100));
                    onProgress(fakePct);
                }
            }
        });
        await withConcurrency(limit, tasks);
    } catch (e) {
        throw e;
    } finally {
        try { zipfile.close(); } catch (_) {}
    }
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
    const entries = [];

    await new Promise((resolve, reject) => {
        zipfile.on('error', reject);
        zipfile.on('end', resolve);
        zipfile.on('entry', entry => {
            entries.push(entry);
            zipfile.readEntry();
        });
        zipfile.readEntry();
    }).catch(e => {
        try { zipfile.close(); } catch (_) {}
        throw e;
    });

    try {
        const tasks = [];
        
        // Phase 1 : Extraction (en isolant d'abord __delta__.json)
        for (const entry of entries) {
            if (entry.fileName === '__delta__.json') {
                const readStream = await openZipStream(zipfile, entry);
                let data = '';
                for await (const chunk of readStream) {
                    data += chunk;
                }
                try { deletedFiles = JSON.parse(data).deletedFiles || []; } catch (_) {}
                continue;
            }

            tasks.push(async () => {
                const dest = path.join(targetPath, entry.fileName);
                const resDest = path.resolve(dest);
                if (!resDest.startsWith(resolvedTarget + path.sep) && resDest !== resolvedTarget) return;

                if (/\/$/.test(entry.fileName)) {
                    await fs.promises.mkdir(dest, { recursive: true });
                } else {
                    await fs.promises.mkdir(path.dirname(dest), { recursive: true });
                    const readStream = await openZipStream(zipfile, entry);
                    const { pipeline } = require('stream/promises');
                    await pipeline(readStream, fs.createWriteStream(dest));
                }
            });
        }
        
        await withConcurrency(limit, tasks);

        // Phase 2 : Suppression différée (Optimisation concurrente par lots via withConcurrency)
        const deleteTasks = deletedFiles.map(relPath => async () => {
            const absPath = path.join(targetPath, relPath.replace(/\//g, path.sep));
            if (!path.resolve(absPath).startsWith(resolvedTarget + path.sep)) return;
            
            if (await existsSafe(absPath)) {
                await safeRm(absPath);
            }
        });
        await withConcurrency(limit, deleteTasks);

    } catch (e) {
        throw e;
    } finally {
        try { zipfile.close(); } catch (_) {}
    }
}

module.exports = {
    verifyZipIntegrity, extractZip, applyDelta
};


async function existsSafe(p) {
    try {
        // Enforce preload sandbox check if it's in renderer context and enforceReadSandbox exists
        if (typeof enforceReadSandbox !== 'undefined') p = enforceReadSandbox(p, true);
        await fs.promises.access(p);
        return true;
    } catch {
        return false;
    }
}
