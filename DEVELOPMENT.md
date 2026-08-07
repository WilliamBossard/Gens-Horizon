# Gens-Horizon — Comprehensive Development Guide

This document thoroughly describes the architecture, synchronization model, cryptographic primitives, and communication protocols of **Gens-Horizon**, the "headless" (GUI-less) Cloud engine of the Gens ecosystem.

---

## 1. Overview & Global Architecture
Gens-Horizon is a standalone CLI engine written in Node.js, designed to run in the background. It is responsible for the bidirectional synchronization of game instances with Cloud providers (Google Drive, Dropbox, OneDrive).

It manages file versioning via a **"Delta Sync"** (differential synchronization) approach and **"Incremental Backups"**, guaranteeing blazing speed and drastic savings in bandwidth and storage.

### Core Components
- **`index.js`**: Main CLI router. Handles commands passed to the binary (e.g., `--sync`, `--login`, `--rollback`). It redirects `console.log` streams to `stderr` to protect the IPC JSON stream.
- **`provider.js` & `/providers`**: Implements the Factory pattern (`getProvider()`). This design makes the engine agnostic to the Cloud used. Each provider inherits a common interface handling OAuth2 authentication, quotas, and file upload/download.
- **`sync.js` / `upload.js`**: The core synchronization engines.
  - `upload.js` identifies modified files, creates a partial archive (Delta), and uploads it to the Cloud.
  - `sync.js` queries the Cloud, downloads missing Deltas, and applies them chronologically locally.
- **`scanner.js`**: The local file scanner. It uses **SHA-256** hashing coupled with fast checks (mtime/size) and a concurrency limiter (`withConcurrency`) to instantly generate an instance Manifest without saturating the disk (EMFILE).
- **`rollback.js`**: Allows restoring a local instance to the exact state of a past Delta, reconstructing the history deterministically.
- **`cloud-operations.js`**: Encapsulates high-level operations on the Cloud Index (remote file list). Notably provides `getCloudIndexAndCleanDuplicates()` which fetches the cloud index and automatically purges obsolete backup duplicates to prevent excessive quota consumption. Used by `sync.js` and `upload.js`. <!-- AUDIT-24 -->
- **`zip-utils.js`**: Utilities for secure ZIP file extraction.
  - Strict protection against **Path Traversal** attacks (`resDest.startsWith(resolvedTarget)`).
  - Verification of the ZIP magic signature (`0x504B0304`) AND the central directory before extraction.
  - Uses **yauzl** for highly memory-optimized decompression. (Note: The engine strictly refuses zips whose central directory is unreadable, without attempting sequential fallback).

---

## 2. Communication Protocol (IPC JSON)
The Horizon engine runs as a child process of the Launcher. It communicates asynchronously via standard input/output streams (`stdout` and `stdin`).

All data emitted on `stdout` is strictly formatted in pure JSON. Example:
```json
{
  "type": "PROGRESS",
  "step": "COMPRESSING",
  "value": 45,
  "instance": "Aventure"
}
```
*Note: All `console.log/warn` emitted by internal libraries are intercepted and sent to `process.stderr` to prevent JSON parsing corruption on the Launcher side.*

Common message types include: `PROGRESS`, `INFO`, `SUCCESS`, `ERROR`, `ROLLBACK_LIST`, `CHECK_RESULT`, `CLOUD_LIST`.

---

## 3. Cryptographic Security & Locks (Security Design)

The application follows NIST recommendations and applies "Defense in Depth":

1. **Hardware-Bound Authentication (`Auth.js`)**:
   OAuth2 tokens are never stored in plaintext. They are encrypted on disk in `AES-256-GCM` (the authenticated GCM mode replaces the old CBC for maximum security).
   The encryption key (32 bytes) is derived as follows:
   - Function: `PBKDF2`
   - Iterations: **600,000** (Current NIST/OWASP standard to block offline brute-forcing).
   - Salt: Random `salt.key` file (16 bytes) generated on first launch, stored with `mode: 0o600`.
   - Password: Content of `.machine_id` — a random **256-bit** (32 bytes) identifier generated once on first launch with `crypto.randomBytes(32)`. Unlike the hostname, this ID cannot be guessed or reproduced on another machine.
   *Stealing the Horizon folder without the physical machine (and its `.machine_id`) makes decryption virtually impossible.*

   Seamless migration: If a token is encrypted with the old format (AES-CBC), it is automatically migrated to AES-GCM upon the first successful decryption.

2. **Synchronization Integrity (`lock.js`)**:
   A lockfile system prevents launching multiple simultaneous synchronization operations (which would corrupt the instance).
   - Mechanism: The `horizon.lock` file contains the PID of the master process. Creation is atomic (`O_CREAT | O_EXCL`).
   - **Heartbeat**: The lock timestamp is updated every 5 seconds (`utimesSync`) to distinguish active processes from zombie processes.
   - **Coupling with the Launcher**: The Launcher (`ipc-horizon.js`) reads the lockfile's `mtimeMs` via `fs.promises.stat()` to check for an active Horizon process. This implicit coupling means the heartbeat interval (5s) and the stale lock threshold (2h) must remain consistent between both projects.
   - "Stale Lock": A lock is considered stale if the PID process is no longer running (`ESRCH`) or if it is older than 2 hours (`STALE_LOCK_MS = 7200000`). It is then purged automatically. If deletion fails (OS error), Horizon gracefully aborts without busy-waiting.

3. **Offline Tolerance**:
   Gens-Horizon is protected against "vacuum" executions. Although the engine autonomously handles transient network errors (adaptive Retry-Strategy), it is the **parent Launcher** that acts as the primary failsafe via `CloudUI.js` by disabling the Horizon execution chain when the machine is declared offline. This prevents the generation of unnecessary error logs and ensures resource conservation.

4. **CI/CD Protection (`config.js`)**:
   OAuth credentials (Client ID / Secret) are not hardcoded. The `config.js` file is generated dynamically at compile time (via GitHub Secrets). If missing or containing dummy keys, the engine will refuse to start to avoid leaking invalid credentials.

---

## 4. Tests and Quality

The project includes test coverage via the native `node:test` module.
- To run tests locally: `npm test`
- Tests verify symmetric cryptography integrity, the adaptive retry system, locks, and hashing with concurrency limiting.
- Continuous Integration (GitHub Actions) runs tests on Linux and Windows environments before any native compilation via `pkg`.
