'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { ZipArchive } = require('archiver');
const { extractZip, applyDelta, verifyZipIntegrity } = require('../zip-utils');

function createSampleZip(zipPath, files) {
    return new Promise((resolve, reject) => {
        const output = fs.createWriteStream(zipPath);
        const archive = new ZipArchive({ zlib: { level: 1 } });
        output.on('close', resolve);
        archive.on('error', reject);
        archive.pipe(output);
        for (const [relPath, content] of Object.entries(files)) {
            archive.append(content, { name: relPath });
        }
        archive.finalize();
    });
}

test('extractZip properly extracts files in subdirectories', async () => {
    const tmpDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-zip-test-'));
    const zipPath = path.join(tmpDir, 'test.zip');
    const extractTarget = path.join(tmpDir, 'extracted');
    await fs.promises.mkdir(extractTarget, { recursive: true });

    try {
        await createSampleZip(zipPath, {
            'instance.json': '{"name":"test"}',
            'mods/test-mod.jar': 'dummy jar content',
            'config/settings.cfg': 'key=value'
        });

        await verifyZipIntegrity(zipPath);
        await extractZip(zipPath, extractTarget);

        const modFile = path.join(extractTarget, 'mods', 'test-mod.jar');
        const stat = await fs.promises.stat(modFile);
        assert.ok(stat.isFile(), 'mods/test-mod.jar should be a file, not a directory');
        const content = await fs.promises.readFile(modFile, 'utf8');
        assert.strictEqual(content, 'dummy jar content');

        const configFile = path.join(extractTarget, 'config', 'settings.cfg');
        const configStat = await fs.promises.stat(configFile);
        assert.ok(configStat.isFile(), 'config/settings.cfg should be a file, not a directory');
        assert.strictEqual(await fs.promises.readFile(configFile, 'utf8'), 'key=value');
    } finally {
        await fs.promises.rm(tmpDir, { recursive: true, force: true });
    }
});

test('applyDelta applies added/modified files and removes deleted files', async () => {
    const tmpDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'horizon-delta-test-'));
    const deltaZipPath = path.join(tmpDir, 'delta.zip');
    const targetDir = path.join(tmpDir, 'instance');
    await fs.promises.mkdir(path.join(targetDir, 'mods'), { recursive: true });

    try {
        // Initial state before delta:
        await fs.promises.writeFile(path.join(targetDir, 'mods', 'old-mod.jar'), 'old content');
        await fs.promises.writeFile(path.join(targetDir, 'mods', 'to-delete.jar'), 'delete me');

        // Delta containing: new mod in subfolder, updated mod, and __delta__.json with deletedFiles
        await createSampleZip(deltaZipPath, {
            '__delta__.json': JSON.stringify({ deletedFiles: ['mods/to-delete.jar'] }),
            'mods/old-mod.jar': 'updated content',
            'mods/new-mod.jar': 'new content'
        });

        await verifyZipIntegrity(deltaZipPath);
        await applyDelta(deltaZipPath, targetDir);

        // Check new-mod.jar
        const newMod = path.join(targetDir, 'mods', 'new-mod.jar');
        const statNew = await fs.promises.stat(newMod);
        assert.ok(statNew.isFile(), 'mods/new-mod.jar should be a file');
        assert.strictEqual(await fs.promises.readFile(newMod, 'utf8'), 'new content');

        // Check old-mod.jar updated
        assert.strictEqual(await fs.promises.readFile(path.join(targetDir, 'mods', 'old-mod.jar'), 'utf8'), 'updated content');

        // Check to-delete.jar deleted
        const existsDeleted = await fs.promises.access(path.join(targetDir, 'mods', 'to-delete.jar')).then(() => true).catch(() => false);
        assert.strictEqual(existsDeleted, false, 'mods/to-delete.jar should have been deleted');
    } finally {
        await fs.promises.rm(tmpDir, { recursive: true, force: true });
    }
});
