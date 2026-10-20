// Package logfile provides bounded, private append-only files for local logs.
package logfile

import (
	"errors"
	"os"
	"runtime"
	"time"
)

const MaxFileBytes = 32 << 20

var ErrLog = errors.New("log unavailable, unsafe, full, or invalid")

// WithLock serializes cooperating processes using an exclusive sibling directory.
// It never breaks a stale lock: a crashed writer requires manual recovery.
func WithLock(path string, fn func() error) (err error) {
	if path == "" {
		return ErrLog
	}
	lock := path + ".lock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = os.Mkdir(lock, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) || time.Now().After(deadline) {
			return ErrLog
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() {
		if os.Remove(lock) != nil {
			err = ErrLog
		}
	}()
	return fn()
}

func CheckFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	// Windows file modes do not establish private ACLs; use a private parent directory.
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) || info.Size() > MaxFileBytes {
		return nil, ErrLog
	}
	return info, nil
}

// AppendLine expects a single encoded JSON value without a trailing newline.
func AppendLine(path string, b []byte, maxRecordBytes int) error {
	if len(b)+1 > maxRecordBytes {
		return ErrLog
	}
	b = append(b, '\n')
	return WithLock(path, func() error {
		info, err := CheckFile(path)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return ErrLog
		}
		flags := os.O_RDWR | os.O_APPEND
		if !exists {
			flags |= os.O_CREATE | os.O_EXCL
		}
		f, err := os.OpenFile(path, flags, 0600)
		if err != nil {
			return ErrLog
		}
		defer f.Close()
		if exists {
			if info.Size()+int64(len(b)) > MaxFileBytes {
				return ErrLog
			}
			if info.Size() > 0 {
				tail := make([]byte, 1)
				if _, err := f.ReadAt(tail, info.Size()-1); err != nil || tail[0] != '\n' {
					return ErrLog
				}
			}
		}
		if n, err := f.Write(b); err != nil || n != len(b) {
			return ErrLog
		}
		if f.Sync() != nil {
			return ErrLog
		}
		if f.Close() != nil {
			return ErrLog
		}
		return nil
	})
}
