//go:build !windows

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const keyringServiceName = "GensHorizonSalt"

func dpapiAvailable() bool {
	return false
}

func dpapiProtect(_ []byte) ([]byte, error) {
	return nil, errors.New("dpapi not supported on this platform")
}

func dpapiUnprotect(_ []byte) ([]byte, error) {
	return nil, errors.New("dpapi not supported on this platform")
}

func osKeyringAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("security")
		return err == nil
	case "linux":
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
			return false
		}
		_, err := exec.LookPath("secret-tool")
		return err == nil
	default:
		return false
	}
}

func osKeyringProtect(saltBytes []byte) ([]byte, error) {
	if len(saltBytes) == 0 {
		return nil, errors.New("keyring: empty salt")
	}
	if !osKeyringAvailable() {
		return nil, errors.New("keyring: unavailable")
	}

	refBuf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, refBuf); err != nil {
		return nil, err
	}
	refID := hex.EncodeToString(refBuf)
	saltHex := hex.EncodeToString(saltBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		// Passer le secret via security -i (stdin) évite d'exposer saltHex dans ps(1)
		cmd := exec.CommandContext(ctx, "security", "-i")
		cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -a %s -s %s -w %s -U\n", refID, keyringServiceName, saltHex))
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, errors.New("security add-generic-password failed: " + strings.TrimSpace(string(out)))
		}
		return []byte(refID), nil
	case "linux":
		cmd := exec.CommandContext(ctx, "secret-tool", "store",
			"--label=GensHorizon Salt", "service", keyringServiceName, "account", refID)
		cmd.Stdin = strings.NewReader(saltHex)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, errors.New("secret-tool store failed: " + strings.TrimSpace(string(out)))
		}
		return []byte(refID), nil
	default:
		return nil, errors.New("keyring: unsupported OS")
	}
}

func osKeyringUnprotect(refBytes []byte) ([]byte, error) {
	refID := strings.TrimSpace(string(refBytes))
	if refID == "" {
		return nil, errors.New("keyring: empty reference id")
	}
	if !osKeyringAvailable() {
		return nil, errors.New("keyring: unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "security", "find-generic-password",
			"-a", refID, "-s", keyringServiceName, "-w")
	case "linux":
		cmd = exec.CommandContext(ctx, "secret-tool", "lookup",
			"service", keyringServiceName, "account", refID)
	default:
		return nil, errors.New("keyring: unsupported OS")
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil || len(raw) < 16 {
		return nil, errors.New("keyring: invalid stored salt format")
	}
	return raw, nil
}
