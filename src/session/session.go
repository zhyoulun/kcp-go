package session

import (
	"crypto/rand"
	"encoding/binary"
	"github.com/pkg/errors"
	"github.com/zhyoulun/kcp-go/src/constant"
	"github.com/zhyoulun/kcp-go/src/kcp"
	"github.com/zhyoulun/kcp-go/src/util"
	"golang.org/x/net/ipv4"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type (
	// UDPSession defines a KCP session implemented by UDP
	UDPSession struct {
		conn    net.PacketConn // the underlying packet connection
		ownConn bool           // true if we created conn internally, false if provided by caller
		Kcp     *kcp.KCP       // KCP ARQ protocol
		l       ListenerI      // pointing to the Listener object if it's been accepted by a Listener
		//block   BlockCrypt     // block encryption object

		// kcp receiving is based on packets
		// recvbuf turns packets into stream
		recvbuf []byte
		bufptr  []byte

		//// FEC codec
		//fecDecoder *fecDecoder
		//fecEncoder *fecEncoder

		// settings
		remote net.Addr  // remote peer address
		rd     time.Time // read deadline
		wd     time.Time // write deadline
		//headerSize int       // the header size additional to a KCP frame
		//ackNoDelay bool      // send ack immediately for each incoming packet(testing purpose)
		//writeDelay bool // delay kcp.flush() for Write() for bulk transfer
		//dup        int  // duplicate udp packets(testing purpose)

		// notifications
		die          chan struct{} // notify current session has Closed
		dieOnce      sync.Once
		chReadEvent  chan struct{} // notify Read() can be called without blocking
		chWriteEvent chan struct{} // notify Write() can be called without blocking

		// socket error handling
		socketReadError      atomic.Value
		socketWriteError     atomic.Value
		chSocketReadError    chan struct{}
		chSocketWriteError   chan struct{}
		socketReadErrorOnce  sync.Once
		socketWriteErrorOnce sync.Once

		// nonce generator
		//nonce Entropy

		// packets waiting to be sent on wire
		txqueue []ipv4.Message //发送队列
		//xconn           batchConn // for x/net
		xconnWriteError error

		mu sync.Mutex
	}

	//setReadBuffer interface {
	//	SetReadBuffer(bytes int) error
	//}
	//
	//setWriteBuffer interface {
	//	SetWriteBuffer(bytes int) error
	//}
	//
	//setDSCP interface {
	//	SetDSCP(int) error
	//}
)

type ListenerI interface {
	CloseSession(net.Addr) bool
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
func DialWithOptions(raddr string) (*UDPSession, error) {
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
	return NewUDPSession(convid, nil, conn, true, udpaddr), nil
}

// newUDPSession create a new udp session for client or server
//func newUDPSession(conv uint32, dataShards, parityShards int, l *Listener, conn net.PacketConn, ownConn bool, remote net.Addr, block BlockCrypt) *UDPSession {

func NewUDPSession(conv uint32, l ListenerI, conn net.PacketConn, ownConn bool, remote net.Addr) *UDPSession {
	sess := new(UDPSession)
	sess.die = make(chan struct{})
	//sess.nonce = new(nonceAES128)
	//sess.nonce.Init()
	sess.chReadEvent = make(chan struct{}, 1)
	sess.chWriteEvent = make(chan struct{}, 1)
	sess.chSocketReadError = make(chan struct{})
	sess.chSocketWriteError = make(chan struct{})
	sess.remote = remote
	sess.conn = conn
	sess.ownConn = ownConn
	sess.l = l
	//sess.block = block
	sess.recvbuf = make([]byte, constant.MtuLimit)

	//// cast to writebatch conn
	//if _, ok := conn.(*net.UDPConn); ok {
	//	addr, err := net.ResolveUDPAddr("udp", conn.LocalAddr().String())
	//	if err == nil {
	//		if addr.IP.To4() != nil {
	//			sess.xconn = ipv4.NewPacketConn(conn)
	//		} else {
	//			sess.xconn = ipv6.NewPacketConn(conn)
	//		}
	//	}
	//}

	//// FEC codec initialization
	//sess.fecDecoder = newFECDecoder(dataShards, parityShards)
	//if sess.block != nil {
	//	sess.fecEncoder = newFECEncoder(dataShards, parityShards, cryptHeaderSize)
	//} else {
	//	sess.fecEncoder = newFECEncoder(dataShards, parityShards, 0)
	//}

	//// calculate additional header size introduced by FEC and encryption
	//if sess.block != nil {
	//	sess.headerSize += cryptHeaderSize
	//}
	//if sess.fecEncoder != nil {
	//	sess.headerSize += fecHeaderSizePlus2
	//}

	sess.Kcp = kcp.NewKCP(conv, func(buf []byte, size int) {
		//if size >= IKCP_OVERHEAD+sess.headerSize {
		if size >= kcp.IKCP_OVERHEAD {
			sess.output(buf[:size])
		}
	})
	//sess.kcp.ReserveBytes(sess.headerSize)

	if sess.l == nil { // it's a client connection
		go sess.readLoop()
		//atomic.AddUint64(&DefaultSnmp.ActiveOpens, 1)
	}
	//else {
	//	//atomic.AddUint64(&DefaultSnmp.PassiveOpens, 1)
	//}

	// start per-session updater
	util.SystemTimedSched.Put(sess.update, time.Now())

	//currestab := atomic.AddUint64(&DefaultSnmp.CurrEstab, 1)
	//maxconn := atomic.LoadUint64(&DefaultSnmp.MaxConn)
	//if currestab > maxconn {
	//	atomic.CompareAndSwapUint64(&DefaultSnmp.MaxConn, maxconn, currestab)
	//}

	return sess
}

// Read implements net.Conn
func (s *UDPSession) Read(b []byte) (n int, err error) {
	for {
		s.mu.Lock()
		if len(s.bufptr) > 0 { // copy from buffer into b
			n = copy(b, s.bufptr)
			s.bufptr = s.bufptr[n:]
			s.mu.Unlock()
			//atomic.AddUint64(&DefaultSnmp.BytesReceived, uint64(n))
			return n, nil
		}

		if size := s.Kcp.PeekSize(); size > 0 { // peek data size from kcp
			if len(b) >= size { // receive data into 'b' directly
				s.Kcp.Recv(b)
				s.mu.Unlock()
				//atomic.AddUint64(&DefaultSnmp.BytesReceived, uint64(size))
				return size, nil
			}

			// if necessary resize the stream buffer to guarantee a sufficient buffer space
			if cap(s.recvbuf) < size {
				s.recvbuf = make([]byte, size)
			}

			// resize the length of recvbuf to correspond to data size
			s.recvbuf = s.recvbuf[:size]
			s.Kcp.Recv(s.recvbuf)
			n = copy(b, s.recvbuf)   // copy to 'b'
			s.bufptr = s.recvbuf[n:] // pointer update
			s.mu.Unlock()
			//atomic.AddUint64(&DefaultSnmp.BytesReceived, uint64(n))
			return n, nil
		}

		// deadline for current reading operation
		var timeout *time.Timer
		var c <-chan time.Time
		if !s.rd.IsZero() {
			if time.Now().After(s.rd) {
				s.mu.Unlock()
				return 0, errors.WithStack(constant.ErrTimeout)
			}

			delay := time.Until(s.rd)
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		// wait for read event or timeout or error
		select {
		case <-s.chReadEvent:
			if timeout != nil {
				timeout.Stop()
			}
		case <-c:
			return 0, errors.WithStack(constant.ErrTimeout)
		case <-s.chSocketReadError:
			return 0, s.socketReadError.Load().(error)
		case <-s.die:
			return 0, errors.WithStack(io.ErrClosedPipe)
		}
	}
}

// Write implements net.Conn
func (s *UDPSession) Write(b []byte) (n int, err error) { return s.WriteBuffers([][]byte{b}) }

// WriteBuffers write a vector of byte slices to the underlying connection
func (s *UDPSession) WriteBuffers(v [][]byte) (n int, err error) {
	for {
		select {
		case <-s.chSocketWriteError:
			return 0, s.socketWriteError.Load().(error)
		case <-s.die:
			return 0, errors.WithStack(io.ErrClosedPipe)
		default:
		}

		s.mu.Lock()

		// make sure write do not overflow the max sliding window on both side
		waitsnd := s.Kcp.WaitSnd()
		if waitsnd < int(kcp.IKCP_WND_SND) && waitsnd < int(s.Kcp.Rmt_wnd) {
			for _, b := range v {
				n += len(b)
				for {
					if len(b) <= kcp.IKCP_MSS {
						s.Kcp.Send(b)
						break
					} else {
						s.Kcp.Send(b[:kcp.IKCP_MSS])
						b = b[kcp.IKCP_MSS:]
					}
				}
			}

			waitsnd = s.Kcp.WaitSnd()
			//if waitsnd >= int(s.kcp.snd_wnd) || waitsnd >= int(s.kcp.rmt_wnd) || !s.writeDelay {
			if waitsnd >= kcp.IKCP_WND_SND || waitsnd >= int(s.Kcp.Rmt_wnd) {
				//s.kcp.flush(false)
				s.Kcp.Flush()
				s.uncork()
			}
			s.mu.Unlock()
			//atomic.AddUint64(&DefaultSnmp.BytesSent, uint64(n))
			return n, nil
		}

		var timeout *time.Timer
		var c <-chan time.Time
		if !s.wd.IsZero() {
			if time.Now().After(s.wd) {
				s.mu.Unlock()
				return 0, errors.WithStack(constant.ErrTimeout)
			}
			delay := time.Until(s.wd)
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		select {
		case <-s.chWriteEvent:
			if timeout != nil {
				timeout.Stop()
			}
		case <-c:
			return 0, errors.WithStack(constant.ErrTimeout)
		case <-s.chSocketWriteError:
			return 0, s.socketWriteError.Load().(error)
		case <-s.die:
			return 0, errors.WithStack(io.ErrClosedPipe)
		}
	}
}

// uncork sends data in txqueue if there is any
// 将txqueue中的数据发送出去
func (s *UDPSession) uncork() {
	if len(s.txqueue) > 0 {
		s.tx(s.txqueue)
		// recycle
		for k := range s.txqueue {
			util.XmitBuf.Put(s.txqueue[k].Buffers[0])
			s.txqueue[k].Buffers = nil
		}
		s.txqueue = s.txqueue[:0]
	}
}

// Close closes the connection.
func (s *UDPSession) Close() error {
	var once bool
	s.dieOnce.Do(func() {
		close(s.die)
		once = true
	})

	if once {
		//atomic.AddUint64(&DefaultSnmp.CurrEstab, ^uint64(0))

		// try best to send all queued messages
		s.mu.Lock()
		//s.kcp.flush(false)
		s.Kcp.Flush()
		s.uncork()
		// release pending segments
		s.Kcp.ReleaseTX()
		//if s.fecDecoder != nil {
		//	s.fecDecoder.release()
		//}
		s.mu.Unlock()

		if s.l != nil { // belongs to listener
			s.l.CloseSession(s.remote)
			return nil
		} else if s.ownConn { // client socket close
			return s.conn.Close()
		} else {
			return nil
		}
	} else {
		return errors.WithStack(io.ErrClosedPipe)
	}
}

// LocalAddr returns the local network address. The Addr returned is shared by all invocations of LocalAddr, so do not modify it.
func (s *UDPSession) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// RemoteAddr returns the remote network address. The Addr returned is shared by all invocations of RemoteAddr, so do not modify it.
func (s *UDPSession) RemoteAddr() net.Addr { return s.remote }

// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.

func (s *UDPSession) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.wd = t
	s.notifyReadEvent()
	s.notifyWriteEvent()
	return nil
}

// SetReadDeadline implements the Conn SetReadDeadline method.
func (s *UDPSession) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.notifyReadEvent()
	return nil
}

// SetWriteDeadline implements the Conn SetWriteDeadline method.
func (s *UDPSession) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wd = t
	s.notifyWriteEvent()
	return nil
}

//// SetWriteDelay delays write for bulk transfer until the next update interval
//func (s *UDPSession) SetWriteDelay(delay bool) {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	s.writeDelay = delay
//}

//// SetWindowSize set maximum window size
//func (s *UDPSession) SetWindowSize(sndwnd, rcvwnd int) {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	s.kcp.WndSize(sndwnd, rcvwnd)
//}

//// SetMtu sets the maximum transmission unit(not including UDP header)
//func (s *UDPSession) SetMtu(mtu int) bool {
//	if mtu > mtuLimit {
//		return false
//	}
//
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	s.kcp.SetMtu(mtu)
//	return true
//}

//// SetStreamMode toggles the stream mode on/off
//func (s *UDPSession) SetStreamMode(enable bool) {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	if enable {
//		s.kcp.stream = 1
//	} else {
//		s.kcp.stream = 0
//	}
//}

//// SetACKNoDelay changes ack flush option, set true to flush ack immediately,
//func (s *UDPSession) SetACKNoDelay(nodelay bool) {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	s.ackNoDelay = nodelay
//}

//// (deprecated)
////
//// SetDUP duplicates udp packets for kcp output.
//func (s *UDPSession) SetDUP(dup int) {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	s.dup = dup
//}

//// SetNoDelay calls nodelay() of kcp
//// https://github.com/skywind3000/kcp/blob/master/README.en.md#protocol-configuration
//func (s *UDPSession) SetNoDelay(nodelay, interval, resend, nc int) {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	s.kcp.NoDelay(nodelay, interval, resend, nc)
//}

//// SetDSCP sets the 6bit DSCP field in IPv4 header, or 8bit Traffic Class in IPv6 header.
////
//// if the underlying connection has implemented `func SetDSCP(int) error`, SetDSCP() will invoke
//// this function instead.
////
//// It has no effect if it's accepted from Listener.
//func (s *UDPSession) SetDSCP(dscp int) error {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	if s.l != nil {
//		return errInvalidOperation
//	}
//
//	// interface enabled
//	if ts, ok := s.conn.(setDSCP); ok {
//		return ts.SetDSCP(dscp)
//	}
//
//	if nc, ok := s.conn.(net.Conn); ok {
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

//// SetReadBuffer sets the socket read buffer, no effect if it's accepted from Listener
//func (s *UDPSession) SetReadBuffer(bytes int) error {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	if s.l == nil {
//		if nc, ok := s.conn.(setReadBuffer); ok {
//			return nc.SetReadBuffer(bytes)
//		}
//	}
//	return errInvalidOperation
//}
//
//// SetWriteBuffer sets the socket write buffer, no effect if it's accepted from Listener
//func (s *UDPSession) SetWriteBuffer(bytes int) error {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	if s.l == nil {
//		if nc, ok := s.conn.(setWriteBuffer); ok {
//			return nc.SetWriteBuffer(bytes)
//		}
//	}
//	return errInvalidOperation
//}

// post-processing for sending a packet from kcp core
// steps:
// 1. FEC packet generation
// 2. CRC32 integrity
// 3. Encryption
// 4. TxQueue
func (s *UDPSession) output(buf []byte) {
	//var ecc [][]byte

	//// 1. FEC encoding
	//if s.fecEncoder != nil {
	//	ecc = s.fecEncoder.encode(buf)
	//}

	//// 2&3. crc32 & encryption
	//if s.block != nil {
	//	s.nonce.Fill(buf[:nonceSize])
	//	checksum := crc32.ChecksumIEEE(buf[cryptHeaderSize:])
	//	binary.LittleEndian.PutUint32(buf[nonceSize:], checksum)
	//	s.block.Encrypt(buf, buf)
	//
	//	for k := range ecc {
	//		s.nonce.Fill(ecc[k][:nonceSize])
	//		checksum := crc32.ChecksumIEEE(ecc[k][cryptHeaderSize:])
	//		binary.LittleEndian.PutUint32(ecc[k][nonceSize:], checksum)
	//		s.block.Encrypt(ecc[k], ecc[k])
	//	}
	//}

	// 4. TxQueue
	var msg ipv4.Message
	//for i := 0; i < s.dup+1; i++ {
	bts := util.XmitBuf.Get().([]byte)[:len(buf)]
	copy(bts, buf)
	msg.Buffers = [][]byte{bts}
	msg.Addr = s.remote
	s.txqueue = append(s.txqueue, msg)
	//}

	//for k := range ecc {
	//	bts := xmitBuf.Get().([]byte)[:len(ecc[k])]
	//	copy(bts, ecc[k])
	//	msg.Buffers = [][]byte{bts}
	//	msg.Addr = s.remote
	//	s.txqueue = append(s.txqueue, msg)
	//}
}

// sess update to trigger protocol
func (s *UDPSession) update() {
	select {
	case <-s.die:
	default:
		s.mu.Lock()
		//interval := s.kcp.flush(false)
		interval := s.Kcp.Flush()
		waitsnd := s.Kcp.WaitSnd()
		if waitsnd < kcp.IKCP_WND_SND && waitsnd < int(s.Kcp.Rmt_wnd) {
			s.notifyWriteEvent()
		}
		s.uncork()
		s.mu.Unlock()
		// self-synchronized timed scheduling
		util.SystemTimedSched.Put(s.update, time.Now().Add(time.Duration(interval)*time.Millisecond))
	}
}

//// GetConv gets conversation id of a session
//func (s *UDPSession) GetConv() uint32 { return s.kcp.conv }

//// GetRTO gets current rto of the session
//func (s *UDPSession) GetRTO() uint32 {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	return s.kcp.rx_rto
//}

//// GetSRTT gets current srtt of the session
//func (s *UDPSession) GetSRTT() int32 {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	return s.kcp.rx_srtt
//}

//// GetRTTVar gets current rtt variance of the session
//func (s *UDPSession) GetSRTTVar() int32 {
//	s.mu.Lock()
//	defer s.mu.Unlock()
//	return s.kcp.rx_rttvar
//}

func (s *UDPSession) notifyReadEvent() {
	select {
	case s.chReadEvent <- struct{}{}:
	default:
	}
}

func (s *UDPSession) notifyWriteEvent() {
	select {
	case s.chWriteEvent <- struct{}{}:
	default:
	}
}

func (s *UDPSession) NotifyReadError(err error) {
	s.socketReadErrorOnce.Do(func() {
		s.socketReadError.Store(err)
		close(s.chSocketReadError)
	})
}

func (s *UDPSession) notifyWriteError(err error) {
	s.socketWriteErrorOnce.Do(func() {
		s.socketWriteError.Store(err)
		close(s.chSocketWriteError)
	})
}

// packet input stage
func (s *UDPSession) packetInput(data []byte) {
	//decrypted := false
	//if s.block != nil && len(data) >= cryptHeaderSize {
	//	s.block.Decrypt(data, data)
	//	data = data[nonceSize:]
	//	checksum := crc32.ChecksumIEEE(data[crcSize:])
	//	if checksum == binary.LittleEndian.Uint32(data) {
	//		data = data[crcSize:]
	//		decrypted = true
	//	} else {
	//		atomic.AddUint64(&DefaultSnmp.InCsumErrors, 1)
	//	}
	//} else if s.block == nil {
	//	decrypted = true
	//}
	//decrypted = true

	//if decrypted && len(data) >= IKCP_OVERHEAD {
	if len(data) >= kcp.IKCP_OVERHEAD {
		s.KcpInput(data)
	}
}

//这里的data已经去掉了nonce和crc32
func (s *UDPSession) KcpInput(data []byte) {
	//var kcpInErrors, fecErrs, fecRecovered, fecParityShards uint64

	//fecFlag := binary.LittleEndian.Uint16(data[4:])
	//if fecFlag == typeData || fecFlag == typeParity { // 16bit kcp cmd [81-84] and frg [0-255] will not overlap with FEC type 0x00f1 0x00f2
	//if len(data) >= fecHeaderSizePlus2 {
	//	f := fecPacket(data)
	//	if f.flag() == typeParity {
	//		fecParityShards++
	//	}
	//
	//	// lock
	//	s.mu.Lock()
	//	//// if fecDecoder is not initialized, create one with default parameter
	//	//if s.fecDecoder == nil {
	//	//	s.fecDecoder = newFECDecoder(1, 1)
	//	//}
	//	//recovers := s.fecDecoder.decode(f)
	//	if f.flag() == typeData {
	//		if ret := s.kcp.Input(data[fecHeaderSizePlus2:], true, s.ackNoDelay); ret != 0 {
	//			kcpInErrors++
	//		}
	//	}
	//
	//	//for _, r := range recovers {
	//	//	if len(r) >= 2 { // must be larger than 2bytes
	//	//		sz := binary.LittleEndian.Uint16(r)
	//	//		if int(sz) <= len(r) && sz >= 2 {
	//	//			if ret := s.kcp.Input(r[2:sz], false, s.ackNoDelay); ret == 0 {
	//	//				fecRecovered++
	//	//			} else {
	//	//				kcpInErrors++
	//	//			}
	//	//		} else {
	//	//			fecErrs++
	//	//		}
	//	//	} else {
	//	//		fecErrs++
	//	//	}
	//	//	// recycle the recovers
	//	//	xmitBuf.Put(r)
	//	//}
	//
	//	// to notify the readers to receive the data
	//	if n := s.kcp.PeekSize(); n > 0 {
	//		s.notifyReadEvent()
	//	}
	//	// to notify the writers
	//	waitsnd := s.kcp.WaitSnd()
	//	if waitsnd < int(s.kcp.snd_wnd) && waitsnd < int(s.kcp.rmt_wnd) {
	//		s.notifyWriteEvent()
	//	}
	//
	//	s.uncork()
	//	s.mu.Unlock()
	//} else {
	//	atomic.AddUint64(&DefaultSnmp.InErrs, 1)
	//}
	//} else {
	s.mu.Lock()
	//if ret := s.kcp.Input(data, true, s.ackNoDelay); ret != 0 {
	if ret := s.Kcp.Input(data); ret != 0 {
		//kcpInErrors++
	}
	if n := s.Kcp.PeekSize(); n > 0 {
		s.notifyReadEvent()
	}
	waitsnd := s.Kcp.WaitSnd()
	if waitsnd < kcp.IKCP_WND_SND && waitsnd < int(s.Kcp.Rmt_wnd) {
		s.notifyWriteEvent()
	}
	s.uncork()
	s.mu.Unlock()
	//}

	//atomic.AddUint64(&DefaultSnmp.InPkts, 1)
	//atomic.AddUint64(&DefaultSnmp.InBytes, uint64(len(data)))
	//if fecParityShards > 0 {
	//	atomic.AddUint64(&DefaultSnmp.FECParityShards, fecParityShards)
	//}
	//if kcpInErrors > 0 {
	//	atomic.AddUint64(&DefaultSnmp.KCPInErrors, kcpInErrors)
	//}
	//if fecErrs > 0 {
	//	atomic.AddUint64(&DefaultSnmp.FECErrs, fecErrs)
	//}
	//if fecRecovered > 0 {
	//	atomic.AddUint64(&DefaultSnmp.FECRecovered, fecRecovered)
	//}

}

func (s *UDPSession) tx(txqueue []ipv4.Message) {
	s.defaultTx(txqueue)
}

func (s *UDPSession) defaultTx(txqueue []ipv4.Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		if n, err := s.conn.WriteTo(txqueue[k].Buffers[0], txqueue[k].Addr); err == nil {
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(errors.WithStack(err))
			break
		}
	}
	//atomic.AddUint64(&DefaultSnmp.OutPkts, uint64(npkts))
	//atomic.AddUint64(&DefaultSnmp.OutBytes, uint64(nbytes))
}

//client read loop
func (s *UDPSession) readLoop() {
	buf := make([]byte, constant.MtuLimit)
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
			s.NotifyReadError(errors.WithStack(err))
			return
		}
	}
}
