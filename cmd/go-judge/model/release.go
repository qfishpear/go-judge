package model

import (
	"os"

	"github.com/criyle/go-judge/worker"
)

// ReleaseResponse deletes the cached file ids and temp files of a worker
// response that was not delivered to the caller. It is safe to call after
// ConvertResponse: FileIDs is shared with the converted results, and closing
// or removing temp files that ConvertResponse already removed is a no-op.
func ReleaseResponse(rt worker.Response, removeID func(string)) {
	for _, res := range rt.Results {
		for _, id := range res.FileIDs {
			removeID(id)
		}
		for _, f := range res.Files {
			f.Close()
			os.Remove(f.Name())
		}
	}
}
