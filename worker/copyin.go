package worker

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/filestore"
)

const copyInMountPath = "/copyin"

// copyInSession owns the staging links created for one worker request. A group
// shares one session so the same cached inode only needs one staging link.
type copyInSession struct {
	root      string
	token     string
	hostDir   string
	links     map[string]string
	openFiles []*os.File
}

func newCopyInSession(root string) *copyInSession {
	return &copyInSession{
		root:  root,
		links: make(map[string]string),
	}
}

func (s *copyInSession) close() {
	for _, f := range s.openFiles {
		_ = f.Close()
	}
	if s.hostDir != "" {
		_ = os.RemoveAll(s.hostDir)
	}
}

func (s *copyInSession) ensureDir() error {
	if s.hostDir != "" {
		return nil
	}
	s.token = rand.Text()
	s.hostDir = filepath.Join(s.root, s.token)
	if err := os.Mkdir(s.hostDir, 0o711); err != nil {
		s.hostDir = ""
		s.token = ""
		return err
	}
	return nil
}

func (s *copyInSession) prepare(
	fs filestore.FileStore,
	file *CachedFile,
	tryLink bool,
) (envexec.File, string, error) {
	if tryLink && s.root != "" {
		if target, ok := s.links[file.FileID]; ok {
			return nil, target, nil
		}
		if err := s.ensureDir(); err == nil {
			index := len(s.links)
			hostTarget := filepath.Join(s.hostDir, fmt.Sprintf("%d", index))
			result, err := fs.PrepareCopyIn(file.FileID, hostTarget)
			if err != nil {
				return nil, "", translateCopyInError(file.FileID, err)
			}
			if result.Linked {
				containerTarget := filepath.Join(copyInMountPath, s.token, fmt.Sprintf("%d", index))
				s.links[file.FileID] = containerTarget
				return nil, containerTarget, nil
			}
			s.trackOpened(result.File)
			return result.File, "", nil
		}
	}

	result, err := fs.PrepareCopyIn(file.FileID, "")
	if err != nil {
		return nil, "", translateCopyInError(file.FileID, err)
	}
	s.trackOpened(result.File)
	return result.File, "", nil
}

func (s *copyInSession) trackOpened(file envexec.File) {
	if f, ok := file.(*envexec.FileOpened); ok {
		s.openFiles = append(s.openFiles, f.File)
	}
}

func translateCopyInError(fileID string, err error) error {
	if errors.Is(err, filestore.ErrFileNotFound) {
		return fmt.Errorf("file does not exist with id %q: %w", fileID, ErrFileNotFound)
	}
	return err
}
