package ftp

import (
	"errors"
	"io"
	"net/textproto"
	"testing"
)

func TestRetrieveError_550IsNotFound(t *testing.T) {
	err := retrieveError(&textproto.Error{Code: 550, Msg: "No such file or directory"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("550 -> %v; want ErrNotFound", err)
	}
}

// Every RETR failure used to read as "stamp absent", so a dropped
// connection made the target look undeployed and rollops re-uploaded it,
// the FTP form of the 2026-10-24 incident.
func TestRetrieveError_OtherFailuresAreErrors(t *testing.T) {
	for _, in := range []error{
		&textproto.Error{Code: 421, Msg: "Service not available"},
		&textproto.Error{Code: 530, Msg: "Not logged in"},
		&textproto.Error{Code: 451, Msg: "Local error in processing"},
		io.ErrUnexpectedEOF,
	} {
		err := retrieveError(in)
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%v -> %v; want a non-NotFound error", in, err)
		}
	}
}
