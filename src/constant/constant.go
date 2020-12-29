package constant

import "github.com/pkg/errors"

const (
	//// 16-bytes nonce for each packet
	//nonceSize = 16
	//
	//// 4-bytes packet checksum
	//crcSize = 4

	//// overall crypto header size
	//cryptHeaderSize = nonceSize + crcSize

	// maximum packet size
	MtuLimit = 1500

	// accept backlog
	AcceptBacklog = 128
)

var (
	//errInvalidOperation = errors.New("invalid operation")
	ErrTimeout = errors.New("timeout")
)