# Gens-Horizon

A cloud synchronization service for Minecraft players. Gens-Horizon is the official, high-performance headless cloud synchronization engine for the Gens ecosystem. It is designed to run in the background (CLI) and securely sync your local game instances with major cloud storage providers like Google Drive, Dropbox, and OneDrive.

## Key Features

- **Delta Sync Technology:** Only uploads and downloads the modified parts of files, drastically reducing bandwidth and saving cloud storage space.
- **Incremental Backups:** Keep a full history of your instance versions and roll back to any previous state deterministically.
- **Enterprise-Grade Security:** Utilizes PBKDF2 (600,000 iterations) with AES-256-GCM encryption. All OAuth2 tokens are hardware-bound and securely encrypted on disk.
- **High Performance:** Multi-threaded local file hashing (SHA-256) and highly optimized zip extraction using `yauzl`.
- **Concurrency & Locking:** A robust lockfile mechanism prevents multiple synchronization processes from running simultaneously and corrupting your game data.

## Getting Started

Gens-Horizon operates as a child process of **Gens-Launcher**. However, if you wish to run or compile it standalone for development purposes:

### Prerequisites
- **Node.js** (version 18 or higher recommended)

### Installation
Clone the repository and install dependencies:
```bash
git clone https://github.com/YourUsername/gens-horizon.git
cd gens-horizon
npm install
```

### Running the Engine
Gens-Horizon communicates via strict JSON over `stdout`/`stdin`. Typical commands invoked by the launcher:

```bash
# Check for a new version
node index.js --check

# Perform a cloud synchronization for a specific instance
node index.js --sync --instance "MyInstanceName"

# Log into a cloud provider
node index.js --login google
```

## Security & Architecture
For a deep dive into the cryptographic primitives, process coupling, and architecture design of Horizon, please read the [DEVELOPMENT.md](./DEVELOPMENT.md) guide.

## License
This project is licensed under the MIT License. See the LICENSE file for more details.