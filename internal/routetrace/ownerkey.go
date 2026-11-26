package routetrace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

var errUnsafeKey = errors.New("追踪本地密钥文件权限无法确认")

func readOwnerKey(path string) (*[sha256.Size]byte, error) {
	file, err := openOwnerKey(path, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var body [sha256.Size + 1]byte
	n, err := io.ReadFull(file, body[:])
	if n != sha256.Size || err != io.ErrUnexpectedEOF {
		return nil, errUnsafeKey
	}
	return (*[sha256.Size]byte)(body[:sha256.Size]), nil
}

func createOwnerKey(path string) (*[sha256.Size]byte, error) {
	var key [sha256.Size]byte
	var suffix [16]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		return nil, err
	}
	tmp := path + "." + hex.EncodeToString(suffix[:]) + ".tmp"
	file, err := openOwnerKey(tmp, true)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	_, err = file.Write(key[:])
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := publishOwnerKey(tmp, path); err != nil {
		return nil, err
	}
	return readOwnerKey(path)
}
