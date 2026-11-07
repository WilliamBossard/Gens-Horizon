# Gens-Horizon

A cloud synchronization service for Minecraft players. Gens-Horizon is the official, high-performance headless cloud synchronization engine for the Gens ecosystem. It is designed to run in the background (CLI) and securely sync your local game instances with major cloud storage providers like Google Drive, Dropbox, and OneDrive.

Written in **Go (Golang)** for minimum binary footprint (~7.5 MB), ultra-fast startup (<10 ms), and low memory overhead (~15 MB).

## Key Features

- **Delta Sync Technology:** Only uploads and downloads the modified parts of files, drastically reducing bandwidth and saving cloud storage space.
- **Incremental Backups:** Keep a full history of your instance versions and roll back to any previous state deterministically.
- **Enterprise-Grade Security:** Utilizes PBKDF2 (600,000 iterations) with AES-256-GCM encryption. All OAuth2 tokens are hardware-bound and securely encrypted on disk.
- **High Performance:** Multi-threaded local file hashing (SHA-256) and memory-optimized zip extraction.
- **Concurrency & Locking:** A robust lockfile mechanism with a 5s heartbeat prevents multiple synchronization processes from running simultaneously and corrupting your game data.
- **Zero Runtime Dependencies:** Compiles down to a standalone native binary for Windows, Linux, and macOS without needing Node.js or any external runtime.

## Getting Started

Gens-Horizon operates as a child process of **Gens-Launcher**. However, if you wish to run or compile it standalone for development purposes:

### Prerequisites
- **Go** (version 1.22 or higher recommended)

### Build
```bash
# Build the binary
go build -ldflags="-s -w" -o dist/Horizon.exe .

# Run tests
go test -v ./...
```

### Running the Engine
Gens-Horizon communicates via strict JSON over `stdout`/`stdin`. Typical commands invoked by the launcher:

```bash
# Check for a new version
./dist/Horizon.exe --check

# Perform a cloud synchronization for a specific instance
./dist/Horizon.exe --sync --instance "MyInstanceName"

# Log into a cloud provider
./dist/Horizon.exe --login google
```

## Security & Architecture
For a deep dive into the cryptographic primitives, process coupling, and architecture design of Horizon, please read the [DEVELOPMENT.md](./DEVELOPMENT.md) guide.

## License
This project is licensed under the MIT License. See the LICENSE file for more details.
