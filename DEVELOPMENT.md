# Gens-Horizon — Comprehensive Development Guide

This document thoroughly describes the architecture, synchronization model, cryptographic primitives, and communication protocols of **Gens-Horizon**, the "headless" (GUI-less) Cloud engine of the Gens ecosystem.

---

## 1. Overview & Global Architecture
Gens-Horizon is a standalone CLI engine written in **Go (Golang)**, designed to run in the background. It is responsible for the bidirectional synchronization of game instances with Cloud providers (Google Drive, Dropbox, OneDrive).

It manages file versioning via a **"Delta Sync"** (differential synchronization) approach and **"Incremental Backups"**, guaranteeing blazing speed and drastic savings in bandwidth and storage.

### Core Architecture & Packages
- **`main.go`**: Main CLI router. Dispatches subcommands (`--sync`, `--login`, `--rollback`, `--check`, `--upload`, `--quota`). It listens on `os.Stdin` for graceful `SHUTDOWN` commands sent by `Gens-Launcher`.
- **`ipc/`**: Encapsulates the JSON stream over `os.Stdout`. All diagnostic or debug logs are redirected to `os.Stderr` to protect the IPC JSON stream.
- **`providers/`**: Implements the Factory pattern (`GetProvider()`). Providers (`google.go`, `dropbox.go`, `onedrive.go`) share a common Go interface for OAuth2 tokens, quotas, and upload/download.
- **`operations/`**: High-level commands (`sync.go`, `upload.go`, `check.go`, `quota.go`, `rollback.go`, `login.go`).
  - `upload.go` identifies modified files, creates a partial archive (Delta), and uploads it to the Cloud.
  - `sync.go` queries the Cloud, downloads missing Deltas, and applies them chronologically locally.
  - `cloud_ops.go` purges obsolete backup duplicates on the cloud to prevent excessive quota consumption.
- **`scanner/`**: Local file scanner. Uses **SHA-256** hashing with worker pool concurrency (16 goroutines) and fast checks (`mtime`/`size`) to generate instance Manifests instantly without saturating disk I/O.
- **`ziputils/`**: Safe ZIP compression and extraction.
  - Strict protection against **Path Traversal / Zip-Slip** attacks.
  - Verification of the ZIP magic signature (`0x504B0304`) and central directory before extraction.
- **`auth/`**: Token cryptographic primitives (PBKDF2 600,000 iterations, hardware-bound machine ID, AES-256-GCM).
- **`lock/`**: Process synchronization lock (`horizon.lock`) with 5s heartbeat and stale lock handling.
- **`paths/`**: Environment and data paths resolution for the Gens ecosystem.
- **`retry/`**: Adaptive HTTP retry engine with jitter.

---

## 2. Communication Protocol (IPC JSON)
The Horizon engine runs as a child process of the Launcher. It communicates asynchronously via standard input/output streams (`stdout` and `stdin`).

All data emitted on `stdout` is strictly formatted in pure JSON lines. Example:
```json
{
  "type": "PROGRESS",
  "step": "COMPRESSING",
  "value": 45,
  "instance": "Aventure"
}
```
*Note: All diagnostics or error messages are sent to `os.Stderr` to prevent JSON parsing corruption on the Launcher side.*

Common message types include: `PROGRESS`, `INFO`, `SUCCESS`, `ERROR`, `ROLLBACK_LIST`, `CHECK_RESULT`, `CLOUD_LIST`, `QUOTA`, `OFFLINE`.

---

## 3. Cryptographic Security & Locks (Security Design)

The application follows NIST recommendations and applies "Defense in Depth":

1. **Hardware-Bound Authentication (`internal/auth/auth.go`)**:
   OAuth2 tokens are never stored in plaintext. They are encrypted on disk in `AES-256-GCM`.
   The encryption key (32 bytes) is derived as follows:
   - Function: `PBKDF2`
   - Iterations: **600,000** (Current NIST/OWASP standard to block offline brute-forcing).
   - Salt: Random `salt.key` file (16 bytes) generated on first launch with mode `0600`.
   - Password: Content of `.machine_id` — a random **256-bit** (32 bytes) identifier generated once on first launch with cryptographically secure random bytes. Unlike the hostname, this ID cannot be guessed or reproduced on another machine.

2. **Synchronization Integrity (`internal/lock/lock.go`)**:
   A lockfile system prevents launching multiple simultaneous synchronization operations (which would corrupt the instance).
   - Mechanism: The `horizon.lock` file contains the PID of the master process. Creation is atomic (`O_CREATE | O_EXCL`).
   - **Heartbeat**: The lock timestamp is updated every 5 seconds (`os.Chtimes`) to distinguish active processes from zombie processes.
   - **Coupling with the Launcher**: The Launcher (`ipc-horizon.js`) reads the lockfile's `mtimeMs` via `fs.promises.stat()` to check for an active Horizon process.
   - "Stale Lock": A lock is considered stale if the PID process is no longer running or if the heartbeat is older than 30 seconds.

3. **CI/CD & Secret Injection**:
   OAuth credentials (Client ID / Secret) are not hardcoded. In CI/CD (`.github/workflows/release.yml`), they are injected at compile time via `-ldflags`. In local development, `config.json` (modeled after `config.example.json`) or environment variables can be used.

---

## 4. Tests and Quality

Tests are run using standard Go tooling:
```bash
go test -v ./...
```
Tests verify symmetric cryptography integrity, lock management, multi-threaded scanner performance, and safe zip decompression.
