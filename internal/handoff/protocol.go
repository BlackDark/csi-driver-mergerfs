// Package handoff is the unix-socket protocol between the node plugin (server) and
// the fusermount shim in a Mount Pod (client). mergerfs runs the shim as its
// fusermount helper; the shim asks the plugin to mount and passes the FUSE fd it
// gets back to mergerfs (ADR 0001).
//
// Framing: one ASCII message per line, "\n"-terminated; one request per connection.
//
//	client: mount <base64 fusermount -o string>
//	                server: fd (+SCM_RIGHTS)   the plugin mounted the pool
//	                server: reject <reason>    then closes
//	client: unmount server: ok                 the plugin owns unmounting; no-op
package handoff

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Message is one protocol line without its newline.
type Message string

// Protocol messages.
const (
	MsgMount   Message = "mount"
	MsgUnmount Message = "unmount"
	MsgFD      Message = "fd"
	MsgOK      Message = "ok"
	MsgReject  Message = "reject"
)

// Reject reasons sent by the server.
const (
	ReasonMountExists = "mount-exists" // the pool fd was already handed out
	ReasonProtocol    = "protocol"     // malformed or unknown request
	ReasonMountFailed = "mount-failed" // followed by the error text
)

// ErrRejected matches every *RejectError.
var ErrRejected = errors.New("handoff rejected")

// RejectError carries the server's reject reason.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "handoff rejected: " + e.Reason }

// Is makes errors.Is(err, ErrRejected) true for every *RejectError.
func (e *RejectError) Is(target error) bool { return target == ErrRejected }

// maxLine bounds a line; mount options are short.
const maxLine = 8 << 10

func encode(m Message, arg string) []byte {
	if arg != "" {
		return []byte(string(m) + " " + arg + "\n")
	}
	return []byte(string(m) + "\n")
}

// parseLine splits "msg [arg]" and rejects anything not in the protocol.
func parseLine(line string) (Message, string, error) {
	line = strings.TrimRight(line, "\r\n")
	head, arg, _ := strings.Cut(line, " ")
	switch m := Message(head); m {
	case MsgMount, MsgUnmount, MsgFD, MsgOK, MsgReject:
		return m, arg, nil
	}
	if len(head) > 32 {
		head = head[:32]
	}
	return "", "", fmt.Errorf("handoff: unknown message %q", head)
}

func encodeOpts(opts string) string { return base64.StdEncoding.EncodeToString([]byte(opts)) }

func decodeOpts(arg string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		return "", errors.New("handoff: malformed mount options")
	}
	return string(b), nil
}
