// Package process owns checked I/O and cleanup for child-process fixtures.
package process

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
)

// Must fails the fixture immediately on an unexpected setup or cleanup error.
func Must(err error) {
	if err != nil {
		panic(err)
	}
}

// Close tolerates only the already-closed state of an owned network resource.
func Close(closer io.Closer) {
	if err := closer.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
		panic(err)
	}
}

// Serve treats the documented server-shutdown result as successful teardown.
func Serve(err error) {
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		panic(err)
	}
}

// Write aborts an HTTP exchange when the caller went away mid-response.
func Write(writer http.ResponseWriter, data []byte) {
	if _, err := writer.Write(data); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// Marshal never lets a fixture accidentally publish a zero-length JSON body.
func Marshal(value any) []byte {
	data, err := json.Marshal(value)
	Must(err)
	return data
}
