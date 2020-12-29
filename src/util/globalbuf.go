// Package kcp-go is a Reliable-UDP library for golang.
//
// This library intents to provide a smooth, resilient, ordered,
// error-checked and anonymous delivery of streams over UDP packets.
//
// The interfaces of this package aims to be compatible with
// net.Conn in standard library, but offers powerful features for advanced users.
package util

import (
	"github.com/zhyoulun/kcp-go/src/constant"
	"sync"
)

var (
	// a system-wide packet buffer shared among sending, receiving and FEC
	// to mitigate high-frequency memory allocation for packets, bytes from xmitBuf
	// is aligned to 64bit
	XmitBuf sync.Pool
)

func init() {
	XmitBuf.New = func() interface{} {
		return make([]byte, constant.MtuLimit)
	}
}
