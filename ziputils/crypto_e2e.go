package ziputils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	e2eMagicV1       = "GHE2E1\x00"
	e2eMagicV2       = "GHE2E2\x00"
	e2eSaltLen       = 16
	e2eNonceLen      = 12
	e2eChunkSize     = 64 * 1024
	e2ePbkdf2ItersV1 = 100000
	e2ePbkdf2ItersV2 = 600000
)

var (
	ErrE2EMismatch = errors.New("E2E_KEY_MISMATCH")
	ErrE2EInvalid  = errors.New("E2E_INVALID_FORMAT")
)

// IsE2EEncryptedFile vérifie si le fichier commence par l'en-tête magique GHE2E1 ou GHE2E2.
func IsE2EEncryptedFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	hdr := make([]byte, len(e2eMagicV2))
	n, err := io.ReadFull(f, hdr)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	s := string(hdr[:n])
	return n == len(e2eMagicV2) && (s == e2eMagicV1 || s == e2eMagicV2), nil
}

func deriveE2EKey(passphrase string, salt []byte, iters int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, passphrase, salt, iters, 32)
}

func makeChunkNonce(baseNonce []byte, chunkIdx uint64, isLast bool) []byte {
	nonce := make([]byte, e2eNonceLen)
	copy(nonce, baseNonce)
	var idxBytes [8]byte
	binary.BigEndian.PutUint64(idxBytes[:], chunkIdx)
	for i := 0; i < 8; i++ {
		nonce[4+i] ^= idxBytes[i]
	}
	if isLast {
		nonce[0] ^= 0x80
	}
	return nonce
}

// EncryptFileE2E chiffre srcPath vers dstPath en streaming par blocs AES-256-GCM.
func EncryptFileE2E(srcPath, dstPath, passphrase string) error {
	if passphrase == "" {
		return errors.New("passphrase E2E vide")
	}
	salt := make([]byte, e2eSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	baseNonce := make([]byte, e2eNonceLen)
	if _, err := rand.Read(baseNonce); err != nil {
		return err
	}
	key, err := deriveE2EKey(passphrase, salt, e2ePbkdf2ItersV2)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
			_ = os.Remove(dstPath)
		}
	}()

	if _, err := out.Write([]byte(e2eMagicV2)); err != nil {
		return err
	}
	if _, err := out.Write(salt); err != nil {
		return err
	}
	if _, err := out.Write(baseNonce); err != nil {
		return err
	}

	buf := make([]byte, e2eChunkSize)
	var chunkIdx uint64
	var aad [9]byte
	for {
		n, rErr := io.ReadFull(in, buf)
		isLast := rErr == io.EOF || rErr == io.ErrUnexpectedEOF
		if rErr != nil && !isLast {
			return rErr
		}
		if n == 0 && chunkIdx > 0 && isLast {
			// Marqueur de fin explicite si le fichier est un multiple exact de e2eChunkSize
			binary.BigEndian.PutUint64(aad[:8], chunkIdx)
			aad[8] = 1
			ct := gcm.Seal(nil, makeChunkNonce(baseNonce, chunkIdx, true), nil, aad[:])
			var lenBuf [4]byte
			binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
			if _, err := out.Write(lenBuf[:]); err != nil {
				return err
			}
			if _, err := out.Write(ct); err != nil {
				return err
			}
			break
		}

		binary.BigEndian.PutUint64(aad[:8], chunkIdx)
		if isLast {
			aad[8] = 1
		} else {
			aad[8] = 0
		}
		ct := gcm.Seal(nil, makeChunkNonce(baseNonce, chunkIdx, isLast), buf[:n], aad[:])
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
		if _, err := out.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := out.Write(ct); err != nil {
			return err
		}
		chunkIdx++
		if isLast {
			break
		}
	}

	if err := out.Close(); err != nil {
		return err
	}
	closed = true
	return nil
}

// DecryptFileE2E déchiffre srcPath (format GHE2E1) vers dstPath en vérifiant l'authenticité de chaque bloc.
func DecryptFileE2E(srcPath, dstPath, passphrase string) error {
	if passphrase == "" {
		return ErrE2EMismatch
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	hdr := make([]byte, len(e2eMagicV2)+e2eSaltLen+e2eNonceLen)
	if _, err := io.ReadFull(in, hdr); err != nil {
		return fmt.Errorf("%w: en-tête tronqué", ErrE2EInvalid)
	}
	magicStr := string(hdr[:len(e2eMagicV2)])
	var pbkdf2Iters int
	switch magicStr {
	case e2eMagicV2:
		pbkdf2Iters = e2ePbkdf2ItersV2
	case e2eMagicV1:
		pbkdf2Iters = e2ePbkdf2ItersV1
	default:
		return ErrE2EInvalid
	}
	salt := hdr[len(e2eMagicV2) : len(e2eMagicV2)+e2eSaltLen]
	baseNonce := hdr[len(e2eMagicV2)+e2eSaltLen:]

	key, err := deriveE2EKey(passphrase, salt, pbkdf2Iters)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
			_ = os.Remove(dstPath)
		}
	}()

	var lenBuf [4]byte
	var chunkIdx uint64
	var aad [9]byte
	maxCt := uint32(e2eChunkSize + gcm.Overhead())
	for {
		if _, err := io.ReadFull(in, lenBuf[:]); err != nil {
			return fmt.Errorf("%w: flux tronqué avant la fin", ErrE2EInvalid)
		}
		ctLen := binary.BigEndian.Uint32(lenBuf[:])
		if ctLen == 0 || ctLen > maxCt {
			return fmt.Errorf("%w: taille de bloc invalide (%d)", ErrE2EInvalid, ctLen)
		}
		ct := make([]byte, ctLen)
		if _, err := io.ReadFull(in, ct); err != nil {
			return fmt.Errorf("%w: bloc tronqué", ErrE2EInvalid)
		}

		// Essayer d'abord bloc intermédiaire, puis bloc final
		binary.BigEndian.PutUint64(aad[:8], chunkIdx)
		aad[8] = 0
		if pt, err := gcm.Open(nil, makeChunkNonce(baseNonce, chunkIdx, false), ct, aad[:]); err == nil {
			if len(pt) > 0 {
				if _, wErr := out.Write(pt); wErr != nil {
					return wErr
				}
			}
			chunkIdx++
			continue
		}

		aad[8] = 1
		pt, err := gcm.Open(nil, makeChunkNonce(baseNonce, chunkIdx, true), ct, aad[:])
		if err != nil {
			return ErrE2EMismatch
		}
		if len(pt) > 0 {
			if _, wErr := out.Write(pt); wErr != nil {
				return wErr
			}
		}
		break
	}

	if err := out.Close(); err != nil {
		return err
	}
	closed = true
	return nil
}
