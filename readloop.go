package kcp

import (
	"github.com/pkg/errors"
)

//client read loop
func (s *UDPSession) readLoop() {
	s.defaultReadLoop()
}

func (l *Listener) monitor() {
	l.defaultMonitor()
}

func (s *UDPSession) defaultReadLoop() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			// make sure the packet is from the same source
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src { //?什么场景会出现多个包之间的addr不一致
				//atomic.AddUint64(&DefaultSnmp.InErrs, 1)
				continue
			}
			s.packetInput(buf[:n])
		} else {
			s.notifyReadError(errors.WithStack(err))
			return
		}
	}
}

// 从PacketConn中持续读取数据包，数据包最大mtuLimit=1500
func (l *Listener) defaultMonitor() {
	buf := make([]byte, mtuLimit)
	for {
		if n, remoteAddr, err := l.conn.ReadFrom(buf); err == nil {
			l.packetInput(buf[:n], remoteAddr)
		} else {
			l.notifyReadError(errors.WithStack(err))
			return
		}
	}
}
