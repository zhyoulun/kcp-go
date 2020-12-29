package listener

import (
	"crypto/rand"
	"encoding/binary"
	"github.com/pkg/errors"
	"github.com/zhyoulun/kcp-go/src/constant"
	kcp2 "github.com/zhyoulun/kcp-go/src/kcp"
	"github.com/zhyoulun/kcp-go/src/session"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

type (
	// Listener defines a server which will be waiting to accept incoming connections
	Listener struct {
		//block        BlockCrypt     // block encryption
		//dataShards   int            // FEC data shard
		//parityShards int            // FEC parity shard
		conn net.PacketConn // the underlying packet connection
		//ownConn      bool           // true if we created conn internally, false if provided by caller

		sessions        map[string]*session.UDPSession // all sessions accepted by this Listener
		sessionLock     sync.RWMutex
		chAccepts       chan *session.UDPSession // Listen() backlog
		chSessionClosed chan net.Addr            // session close queue

		die     chan struct{} // notify the listener has closed
		dieOnce sync.Once

		// socket error handling
		socketReadError     atomic.Value
		chSocketReadError   chan struct{}
		socketReadErrorOnce sync.Once

		//rd atomic.Value // read deadline for Accept()
	}
)

// packet input stage
func (l *Listener) packetInput(data []byte, remoteAddr net.Addr) {
	decrypted := false
	//if l.block != nil && len(data) >= cryptHeaderSize {
	//	l.block.Decrypt(data, data)
	//	data = data[nonceSize:]
	//	checksum := crc32.ChecksumIEEE(data[crcSize:])
	//	if checksum == binary.LittleEndian.Uint32(data) {
	//		data = data[crcSize:]
	//		decrypted = true
	//	} else {
	//		atomic.AddUint64(&DefaultSnmp.InCsumErrors, 1)
	//	}
	//} else if l.block == nil {
	//	decrypted = true
	//}
	decrypted = true

	// 这里有一个问题，就是如果data的长度过短，小于24B，就直接被丢弃了
	if decrypted && len(data) >= kcp2.IKCP_OVERHEAD {
		l.sessionLock.RLock()
		s, ok := l.sessions[remoteAddr.String()]
		l.sessionLock.RUnlock()

		var conv, sn uint32
		//convRecovered := false
		//fecFlag := binary.LittleEndian.Uint16(data[4:])
		//if fecFlag == typeData || fecFlag == typeParity { // 16bit kcp cmd [81-84] and frg [0-255] will not overlap with FEC type 0x00f1 0x00f2
		//// packet with FEC
		//if fecFlag == typeData && len(data) >= fecHeaderSizePlus2+IKCP_OVERHEAD {
		//	conv = binary.LittleEndian.Uint32(data[fecHeaderSizePlus2:])
		//	sn = binary.LittleEndian.Uint32(data[fecHeaderSizePlus2+IKCP_SN_OFFSET:])
		//	convRecovered = true
		//}
		//} else {
		// packet without FEC
		conv = binary.LittleEndian.Uint32(data)
		sn = binary.LittleEndian.Uint32(data[kcp2.IKCP_SN_OFFSET:])
		//convRecovered = true
		//}

		if ok { // existing connection
			//if !convRecovered || conv == s.kcp.conv { // parity data or valid conversation
			if conv == s.Kcp.Conv { // parity data or valid conversation
				s.KcpInput(data)
			} else if sn == 0 { // should replace current connection
				s.Close()
				s = nil
			}
		}

		//if s == nil { // new session
		//	if len(l.chAccepts) < cap(l.chAccepts) { // do not let the new sessions overwhelm accept queue
		if s == nil && len(l.chAccepts) < cap(l.chAccepts) {
			//s := newUDPSession(conv, l.dataShards, l.parityShards, l, l.conn, false, remoteAddr, l.block)
			s := session.NewUDPSession(conv, l, l.conn, false, remoteAddr)
			s.KcpInput(data)

			l.sessionLock.Lock()
			l.sessions[remoteAddr.String()] = s
			l.sessionLock.Unlock()

			l.chAccepts <- s
			//}
		}
	}
}

func (l *Listener) notifyReadError(err error) {
	l.socketReadErrorOnce.Do(func() {
		l.socketReadError.Store(err)
		close(l.chSocketReadError)

		// propagate read error to all sessions
		l.sessionLock.RLock()
		for _, s := range l.sessions {
			s.NotifyReadError(err)
		}
		l.sessionLock.RUnlock()
	})
}

//// SetReadBuffer sets the socket read buffer for the Listener
//func (l *Listener) SetReadBuffer(bytes int) error {
//	if nc, ok := l.conn.(setReadBuffer); ok {
//		return nc.SetReadBuffer(bytes)
//	}
//	return errInvalidOperation
//}
//
//// SetWriteBuffer sets the socket write buffer for the Listener
//func (l *Listener) SetWriteBuffer(bytes int) error {
//	if nc, ok := l.conn.(setWriteBuffer); ok {
//		return nc.SetWriteBuffer(bytes)
//	}
//	return errInvalidOperation
//}

//// SetDSCP sets the 6bit DSCP field in IPv4 header, or 8bit Traffic Class in IPv6 header.
////
//// if the underlying connection has implemented `func SetDSCP(int) error`, SetDSCP() will invoke
//// this function instead.
//func (l *Listener) SetDSCP(dscp int) error {
//	// interface enabled
//	if ts, ok := l.conn.(setDSCP); ok {
//		return ts.SetDSCP(dscp)
//	}
//
//	if nc, ok := l.conn.(net.Conn); ok {
//		var succeed bool
//		if err := ipv4.NewConn(nc).SetTOS(dscp << 2); err == nil {
//			succeed = true
//		}
//		if err := ipv6.NewConn(nc).SetTrafficClass(dscp); err == nil {
//			succeed = true
//		}
//
//		if succeed {
//			return nil
//		}
//	}
//	return errInvalidOperation
//}

// Accept implements the Accept method in the Listener interface; it waits for the next call and returns a generic Conn.
func (l *Listener) Accept() (net.Conn, error) {
	return l.AcceptKCP()
}

// AcceptKCP accepts a KCP connection
func (l *Listener) AcceptKCP() (*session.UDPSession, error) {
	//var timeout <-chan time.Time
	//if tdeadline, ok := l.rd.Load().(time.Time); ok && !tdeadline.IsZero() {
	//	timeout = time.After(time.Until(tdeadline))
	//}

	select {
	//case <-timeout:
	//	return nil, errors.WithStack(errTimeout)
	case c := <-l.chAccepts:
		return c, nil
	case <-l.chSocketReadError:
		return nil, l.socketReadError.Load().(error)
	case <-l.die:
		return nil, errors.WithStack(io.ErrClosedPipe)
	}
}

//// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.
//func (l *Listener) SetDeadline(t time.Time) error {
//	l.SetReadDeadline(t)
//	l.SetWriteDeadline(t)
//	return nil
//}
//
//// SetReadDeadline implements the Conn SetReadDeadline method.
//func (l *Listener) SetReadDeadline(t time.Time) error {
//	l.rd.Store(t)
//	return nil
//}
//
//// SetWriteDeadline implements the Conn SetWriteDeadline method.
//func (l *Listener) SetWriteDeadline(t time.Time) error { return errInvalidOperation }

// Close stops listening on the UDP address, and closes the socket
func (l *Listener) Close() error {
	var once bool
	l.dieOnce.Do(func() {
		close(l.die)
		once = true
	})

	var err error
	if once {
		err = l.conn.Close()
	} else {
		err = errors.WithStack(io.ErrClosedPipe)
	}
	return err
}

// closeSession notify the listener that a session has closed
func (l *Listener) CloseSession(remote net.Addr) (ret bool) {
	l.sessionLock.Lock()
	defer l.sessionLock.Unlock()
	if _, ok := l.sessions[remote.String()]; ok {
		delete(l.sessions, remote.String())
		return true
	}
	return false
}

// Addr returns the listener's network address, The Addr returned is shared by all invocations of Addr, so do not modify it.
func (l *Listener) Addr() net.Addr { return l.conn.LocalAddr() }

//// Listen listens for incoming KCP packets addressed to the local address laddr on the network "udp",
//func Listen(laddr string) (net.Listener, error) { return ListenWithOptions(laddr, nil, 0, 0) }

// ListenWithOptions listens for incoming KCP packets addressed to the local address laddr on the network "udp" with packet encryption.
//
// 'block' is the block encryption algorithm to encrypt packets.
//
// 'dataShards', 'parityShards' specify how many parity packets will be generated following the data packets.
//
// Check https://github.com/klauspost/reedsolomon for details
func ListenWithOptions(laddr string) (*Listener, error) {
	udpaddr, err := net.ResolveUDPAddr("udp", laddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	//UDPConn is the implementation of the 'Conn' and 'PacketConn' interfaces
	// - Conn is a generic stream-oriented network connection.
	// - PacketConn is a generic packet-oriented network connection. PacketConn是一个面向packet的网络连接

	// PacketConn是一个接口类型，与Conn的区别在于，Conn是基于流式的网络连接接口，而PacketConn是基于网络包的网络连接接口。
	// 由于其基于网络包的特性，所以实现该接口的类型有IPConn和UDPConn类型，即是网络层的连接类型。
	//PacketConn的与Conn的实现函数的区别在，前者实现的是ReadFrom和WriteTo函数，而后者实现的是Read和Write函数。
	conn, err := net.ListenUDP("udp", udpaddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	//return serveConn(block, dataShards, parityShards, conn)
	return serveConn(conn)
}

//// ServeConn serves KCP protocol for a single packet connection.
//func ServeConn(block BlockCrypt, dataShards, parityShards int, conn net.PacketConn) (*Listener, error) {
//	return serveConn(block, dataShards, parityShards, conn, false)
//}

//func serveConn(block BlockCrypt, dataShards, parityShards int, conn net.PacketConn) (*Listener, error) {

func serveConn(conn net.PacketConn) (*Listener, error) {
	l := new(Listener)
	l.conn = conn
	//l.ownConn = ownConn
	l.sessions = make(map[string]*session.UDPSession)
	l.chAccepts = make(chan *session.UDPSession, constant.AcceptBacklog)
	l.chSessionClosed = make(chan net.Addr)
	l.die = make(chan struct{})
	//l.dataShards = dataShards
	//l.parityShards = parityShards
	//l.block = block
	l.chSocketReadError = make(chan struct{})
	go l.monitor()
	return l, nil
}

// Dial connects to the remote address "raddr" on the network "udp" without encryption and FEC
//func Dial(raddr string) (net.Conn, error) { return DialWithOptions(raddr, nil, 0, 0) }

// DialWithOptions connects to the remote address "raddr" on the network "udp" with packet encryption
//
// 'block' is the block encryption algorithm to encrypt packets.
//
// 'dataShards', 'parityShards' specify how many parity packets will be generated following the data packets.
//
// Check https://github.com/klauspost/reedsolomon for details
func DialWithOptions(raddr string) (*session.UDPSession, error) {
	// network type detection
	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	network := "udp4"
	if udpaddr.IP.To4() == nil {
		network = "udp"
	}

	conn, err := net.ListenUDP(network, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var convid uint32
	binary.Read(rand.Reader, binary.LittleEndian, &convid)
	//return newUDPSession(convid, dataShards, parityShards, nil, conn, true, udpaddr, block), nil
	return session.NewUDPSession(convid, nil, conn, true, udpaddr), nil
}

//// NewConn3 establishes a session and talks KCP protocol over a packet connection.
//func NewConn3(convid uint32, raddr net.Addr, block BlockCrypt, dataShards, parityShards int, conn net.PacketConn) (*UDPSession, error) {
//	return newUDPSession(convid, dataShards, parityShards, nil, conn, false, raddr, block), nil
//}

//// NewConn2 establishes a session and talks KCP protocol over a packet connection.
//func NewConn2(raddr net.Addr, block BlockCrypt, dataShards, parityShards int, conn net.PacketConn) (*UDPSession, error) {
//	var convid uint32
//	binary.Read(rand.Reader, binary.LittleEndian, &convid)
//	return NewConn3(convid, raddr, block, dataShards, parityShards, conn)
//}

//// NewConn establishes a session and talks KCP protocol over a packet connection.
//func NewConn(raddr string, block BlockCrypt, dataShards, parityShards int, conn net.PacketConn) (*UDPSession, error) {
//	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
//	if err != nil {
//		return nil, errors.WithStack(err)
//	}
//	return NewConn2(udpaddr, block, dataShards, parityShards, conn)
//}

//server read loop
func (l *Listener) monitor() {
	l.defaultMonitor()
}

// 从PacketConn中持续读取数据包，数据包最大mtuLimit=1500
func (l *Listener) defaultMonitor() {
	buf := make([]byte, constant.MtuLimit)
	for {
		if n, remoteAddr, err := l.conn.ReadFrom(buf); err == nil {
			l.packetInput(buf[:n], remoteAddr)
		} else {
			l.notifyReadError(errors.WithStack(err))
			return
		}
	}
}
