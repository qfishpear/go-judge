package filestore

import (
	"encoding/base32"
	"errors"
	"math/rand/v2"
	"os"

	"github.com/criyle/go-judge/envexec"
)

var errUniqueIDNotGenerated = errors.New("unique id does not exists after tried 50 times")

// ErrFileNotFound indicates that a referenced file-store entry does not exist.
var ErrFileNotFound = errors.New("file not found")

// CopyInFallbackReason is a bounded reason why a requested hard link was not
// created. It is suitable for use as a metrics label.
type CopyInFallbackReason string

const (
	CopyInFallbackNone        CopyInFallbackReason = ""
	CopyInFallbackNotRegular  CopyInFallbackReason = "not_regular"
	CopyInFallbackCrossDevice CopyInFallbackReason = "cross_device"
	CopyInFallbackPermission  CopyInFallbackReason = "permission"
	CopyInFallbackUnsupported CopyInFallbackReason = "unsupported"
	CopyInFallbackOther       CopyInFallbackReason = "other"
)

// CopyInResult contains either a hard-link success or an already-opened source
// file for the ordinary copy-in fallback.
type CopyInResult struct {
	File           envexec.File
	Linked         bool
	FallbackReason CopyInFallbackReason
}

// FileStore defines interface to store file
type FileStore interface {
	Add(name, path string) (string, error) // Add creates a file with path to the storage, returns id
	Remove(string) bool                    // Remove deletes a file by id
	Get(string) (string, envexec.File)     // Get file by id, nil if not exists
	// PrepareCopyIn validates id while holding the store lock. If linkPath is
	// non-empty it first tries to create a hard link there; otherwise, or when
	// linking is unavailable, it returns an opened source file for copying.
	PrepareCopyIn(id, linkPath string) (CopyInResult, error)
	List() map[string]string // List return all file ids to original name
	New() (*os.File, error)  // Create a temporary file to the file store, can be added through Add to save it
	Close() error            // Close releases resources owned by the file store
}

func generateID() (string, error) {
	const randIDLength = 5
	b := make([]byte, randIDLength)
	r := rand.Int64N(1 << 40)
	b[0] = byte(r)
	b[1] = byte(r >> 8)
	b[2] = byte(r >> 16)
	b[3] = byte(r >> 24)
	b[4] = byte(r >> 32)
	return base32.StdEncoding.EncodeToString(b), nil
}
