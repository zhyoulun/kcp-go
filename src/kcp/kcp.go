package kcp

import (
	"github.com/zhyoulun/kcp-go/src/util"
)

const (
	IKCP_RTO_NODELAY = 30  // no delay min rto   retransmission timeout (RTO)，重传超时。
	IKCP_RTO_MIN     = 100 // normal min rto
	IKCP_RTO_DEF     = 200
	IKCP_RTO_MAX     = 60000
	IKCP_CMD_PUSH    = 81 // cmd: push data
	IKCP_CMD_ACK     = 82 // cmd: ack
	IKCP_CMD_WASK    = 83 // cmd: window probe (ask)
	IKCP_CMD_WINS    = 84 // cmd: window size (tell)
	IKCP_ASK_SEND    = 1  // need to send IKCP_CMD_WASK
	IKCP_ASK_TELL    = 2  // need to send IKCP_CMD_WINS
	IKCP_WND_SND     = 32
	IKCP_WND_RCV     = 32
	IKCP_MTU_DEF     = 1400
	IKCP_ACK_FAST    = 3
	IKCP_INTERVAL    = 100
	IKCP_OVERHEAD    = 24
	IKCP_DEADLINK    = 20
	IKCP_THRESH_INIT = 2
	IKCP_THRESH_MIN  = 2
	IKCP_PROBE_INIT  = 7000   // 7 secs to probe window size
	IKCP_PROBE_LIMIT = 120000 // up to 120 secs to probe window
	IKCP_SN_OFFSET   = 12

	IKCP_MSS = IKCP_MTU_DEF - IKCP_OVERHEAD
)

// output_callback is a prototype which ought capture conn and call conn.Write
type output_callback func(buf []byte, size int)

type SessionI interface {
	Output(buf []byte, size int)
}

// KCP defines a single KCP connection
type KCP struct {
	Conv uint32 //conversation 会话
	//mtu   uint32 //默认值IKCP_MTU_DEF（1400），需要函数SetMtu设置，最大传输单元（英语：Maximum Transmission Unit，缩写MTU）
	//mss   uint32 //默认值kcp.mtu - IKCP_OVERHEAD（24），最大分段大小（Maximum Segment Size）
	//state uint32

	snd_una uint32 //The sender of data keeps track of the oldest unacknowledged sequence number in the variable SND.UNA.
	snd_nxt uint32 //The sender of data keeps track of the next sequence number to use in the variable SND.NXT.
	rcv_nxt uint32 //The receiver of data keeps track of the next sequence number to expect in the variable RCV.NXT.

	ssthresh uint32

	//如果没有查看的需求，这两个变量可以调整为计算rto函数的局部变量
	//rx_rttvar int32
	//rx_srtt   int32

	rx_rto uint32
	//rx_minrto uint32

	//snd_wnd uint32
	rcv_wnd uint32
	Rmt_wnd uint32 //应该是remote_wnd
	cwnd    uint32 //congesion window，拥塞窗口
	probe   uint32

	//interval uint32
	ts_flush uint32

	//nodelay uint32 //是否启用 nodelay模式，0不启用；1启用。   默认值为0
	updated uint32

	ts_probe   uint32
	probe_wait uint32

	//dead_link uint32
	incr uint32

	//fastresend int32
	//nocwnd     int32
	//stream int32

	snd_queue []segment
	rcv_queue []segment //长度受到rcv_wnd的限制
	snd_buf   []segment
	rcv_buf   []segment

	acklist []ackItem

	buffer []byte //初始空间mtu，make([]byte, kcp.mtu)
	//reserved int
	//output output_callback
	sessionI SessionI
}

type ackItem struct {
	sn uint32
	ts uint32
}

// NewKCP create a new kcp state machine
//
// 'conv' must be equal in the connection peers, or else data will be silently rejected.
//
// 'output' function will be called whenever these is data to be sent on wire.

func NewKCP(conv uint32, sessionI SessionI) *KCP {
	kcp := new(KCP)
	kcp.Conv = conv
	//kcp.snd_wnd = IKCP_WND_SND
	kcp.rcv_wnd = IKCP_WND_RCV
	kcp.Rmt_wnd = IKCP_WND_RCV
	//kcp.mtu = IKCP_MTU_DEF
	//kcp.mss = IKCP_MTU_DEF - IKCP_OVERHEAD
	kcp.buffer = make([]byte, IKCP_MTU_DEF)
	kcp.rx_rto = IKCP_RTO_DEF
	//kcp.rx_minrto = IKCP_RTO_MIN
	//kcp.interval = IKCP_INTERVAL
	kcp.ts_flush = IKCP_INTERVAL
	kcp.ssthresh = IKCP_THRESH_INIT
	//kcp.dead_link = IKCP_DEADLINK
	kcp.sessionI = sessionI
	return kcp
}

// newSegment creates a KCP segment
func (kcp *KCP) newSegment(size int) (seg segment) {
	seg.data = util.XmitBuf.Get().([]byte)[:size]
	return
}

// delSegment recycles a KCP segment
func (kcp *KCP) delSegment(seg *segment) {
	if seg.data != nil {
		util.XmitBuf.Put(seg.data)
		seg.data = nil
	}
}

//// ReserveBytes keeps n bytes untouched from the beginning of the buffer,
//// the output_callback function should be aware of this.
////
//// Return false if n >= mss
//
//func (kcp *KCP) ReserveBytes(n int) bool {
//	if n >= int(kcp.mtu-IKCP_OVERHEAD) || n < 0 {
//		return false
//	}
//	kcp.reserved = n
//	kcp.mss = kcp.mtu - IKCP_OVERHEAD - uint32(n)
//	return true
//}

// PeekSize checks the size of next message in the recv queue
func (kcp *KCP) PeekSize() (length int) {
	if len(kcp.rcv_queue) == 0 {
		return -1
	}

	seg := &kcp.rcv_queue[0]
	if seg.frg == 0 {
		return len(seg.data)
	}

	if len(kcp.rcv_queue) < int(seg.frg+1) {
		return -1
	}

	for k := range kcp.rcv_queue {
		seg := &kcp.rcv_queue[k]
		length += len(seg.data)
		if seg.frg == 0 {
			break
		}
	}
	return
}

// Receive data from kcp state machine
//
// Return number of bytes read.
//
// Return -1 when there is no readable data.
//
// Return -2 if len(buffer) is smaller than kcp.PeekSize().
func (kcp *KCP) Recv(buffer []byte) (n int) {
	peeksize := kcp.PeekSize()
	if peeksize < 0 {
		return -1
	}

	if peeksize > len(buffer) {
		return -2
	}

	var fast_recover bool
	if len(kcp.rcv_queue) >= int(kcp.rcv_wnd) {
		fast_recover = true
	}

	// merge fragment
	count := 0
	for k := range kcp.rcv_queue {
		seg := &kcp.rcv_queue[k]
		copy(buffer, seg.data)
		buffer = buffer[len(seg.data):]
		n += len(seg.data)
		count++
		kcp.delSegment(seg)
		if seg.frg == 0 {
			break
		}
	}
	if count > 0 {
		kcp.rcv_queue = kcp.remove_front(kcp.rcv_queue, count)
	}

	// move available data from rcv_buf -> rcv_queue
	count = 0
	for k := range kcp.rcv_buf {
		seg := &kcp.rcv_buf[k]
		if seg.sn == kcp.rcv_nxt && len(kcp.rcv_queue)+count < int(kcp.rcv_wnd) {
			kcp.rcv_nxt++
			count++
		} else {
			break
		}
	}

	if count > 0 {
		kcp.rcv_queue = append(kcp.rcv_queue, kcp.rcv_buf[:count]...)
		kcp.rcv_buf = kcp.remove_front(kcp.rcv_buf, count)
	}

	// fast recover
	if len(kcp.rcv_queue) < int(kcp.rcv_wnd) && fast_recover {
		// ready to send back IKCP_CMD_WINS in ikcp_flush
		// tell remote my window size
		kcp.probe |= IKCP_ASK_TELL
	}
	return
}

// Send is user/upper level send, returns below zero for error
func (kcp *KCP) Send(buffer []byte) int {
	var count int
	if len(buffer) == 0 {
		return -1
	}

	// append to previous segment in streaming mode (if possible)
	//if kcp.stream != 0 {
	//	n := len(kcp.snd_queue)
	//	if n > 0 {
	//		seg := &kcp.snd_queue[n-1]
	//		if len(seg.data) < IKCP_MSS {
	//			capacity := IKCP_MSS - len(seg.data)
	//			extend := capacity
	//			if len(buffer) < capacity {
	//				extend = len(buffer)
	//			}
	//
	//			// grow slice, the underlying cap is guaranteed to
	//			// be larger than kcp.mss
	//			oldlen := len(seg.data)
	//			seg.data = seg.data[:oldlen+extend]
	//			copy(seg.data[oldlen:], buffer)
	//			buffer = buffer[extend:]
	//		}
	//	}
	//
	//	if len(buffer) == 0 {
	//		return 0
	//	}
	//}

	if len(buffer) <= IKCP_MSS {
		count = 1
	} else {
		count = (len(buffer) + IKCP_MSS - 1) / IKCP_MSS
	}

	if count > 255 {
		return -2
	}

	if count == 0 {
		count = 1
	}

	for i := 0; i < count; i++ {
		var size int
		if len(buffer) > IKCP_MSS {
			size = IKCP_MSS
		} else {
			size = len(buffer)
		}
		seg := kcp.newSegment(size)
		copy(seg.data, buffer[:size])
		//if kcp.stream == 0 { // message mode
		//	seg.frg = uint8(count - i - 1)
		//} else { // stream mode
		//	seg.frg = 0
		//}
		seg.frg = uint8(count - i - 1)
		kcp.snd_queue = append(kcp.snd_queue, seg)
		buffer = buffer[size:]
	}
	return 0
}

//更新rto，KCP结构体中rx_rto变量
func (kcp *KCP) update_rto(rtt int32) {
	// https://tools.ietf.org/html/rfc6298
	// srtt: smoothed round-trip time
	// rttvar: round-trip time variation
	var rto uint32
	var rx_srtt int32
	var rx_rttvar int32
	if rx_srtt == 0 {
		rx_srtt = rtt
		rx_rttvar = rtt >> 1
	} else {
		delta := rtt - rx_srtt
		rx_srtt += delta >> 3
		if delta < 0 {
			delta = -delta
		}
		if rtt < rx_srtt-rx_rttvar {
			// if the new RTT sample is below the bottom of the range of
			// what an RTT measurement is expected to be.
			// give an 8x reduced weight versus its normal weighting
			rx_rttvar += (delta - rx_rttvar) >> 5
		} else {
			rx_rttvar += (delta - rx_rttvar) >> 2
		}
	}
	rto = uint32(rx_srtt) + util.KCP_imax_(IKCP_INTERVAL, uint32(rx_rttvar)<<2)
	kcp.rx_rto = util.KCP_ibound_(IKCP_RTO_MIN, rto, IKCP_RTO_MAX) //rx_minrto的值是IKCP_RTO_NODELAY或者IKCP_RTO_MIN
}

func (kcp *KCP) shrink_buf() {
	//如果snd_buf不为空，则snd_una为第一个seg的sn
	//否则如果snd_buf为空，snd_una=snd_nxt
	if len(kcp.snd_buf) > 0 {
		seg := &kcp.snd_buf[0]
		kcp.snd_una = seg.sn
	} else {
		kcp.snd_una = kcp.snd_nxt
	}
}

func (kcp *KCP) parse_ack(sn uint32) {
	//sn<kcp.snd_una已经收到ack，这个ack可以忽略了
	//sn>=kcp.snd_nxt这是一个不合法的ack，忽略
	if util.KCP_itimediff(sn, kcp.snd_una) < 0 || util.KCP_itimediff(sn, kcp.snd_nxt) >= 0 {
		return
	}

	for k := range kcp.snd_buf {
		seg := &kcp.snd_buf[k]
		if sn == seg.sn {
			// mark and free space, but leave the segment here,
			// and wait until `una` to delete this, then we don't
			// have to shift the segments behind forward,
			// which is an expensive operation for large window
			seg.acked = 1
			kcp.delSegment(seg)
			break
		}
		//snd_buf中的seg的sn都是有序的，如果sn比当前的seg.sn小，就不用再往后看了，肯定都是比seg.sn小的
		//sn<seg.sn
		if util.KCP_itimediff(sn, seg.sn) < 0 {
			break
		}
	}
}

func (kcp *KCP) parse_fastack(sn, ts uint32) {
	//sn<kcp.snd_una已经收到ack，这个ack可以忽略了
	//sn>=kcp.snd_nxt这是一个不合法的ack，忽略
	if util.KCP_itimediff(sn, kcp.snd_una) < 0 || util.KCP_itimediff(sn, kcp.snd_nxt) >= 0 {
		return
	}

	for k := range kcp.snd_buf {
		seg := &kcp.snd_buf[k]
		// sn<seg.sn
		if util.KCP_itimediff(sn, seg.sn) < 0 {
			break
			//sn>seg.sn && seg.ts<=ts; ts是什么意思？
		} else if sn != seg.sn && util.KCP_itimediff(seg.ts, ts) <= 0 {
			seg.fastack++
		}
	}
}

//根据对端的ack包中的una信息，清理snd_buf头部的无用数据
func (kcp *KCP) parse_una(una uint32) int {
	count := 0
	for k := range kcp.snd_buf {
		seg := &kcp.snd_buf[k]
		if util.KCP_itimediff(una, seg.sn) > 0 {
			kcp.delSegment(seg)
			count++
		} else {
			break
		}
	}
	if count > 0 {
		kcp.snd_buf = kcp.remove_front(kcp.snd_buf, count)
	}
	return count
}

// ack append
func (kcp *KCP) ack_push(sn, ts uint32) {
	kcp.acklist = append(kcp.acklist, ackItem{sn, ts})
}

// returns true if data has repeated
func (kcp *KCP) parse_data(newseg segment) bool {
	sn := newseg.sn
	//sn>=kcp.rcv_nxt+kcp.rcv_wnd
	//sn<kcp.rcv_nxt
	//sn在接收窗口之外，放弃处理
	if util.KCP_itimediff(sn, kcp.rcv_nxt+kcp.rcv_wnd) >= 0 ||
		util.KCP_itimediff(sn, kcp.rcv_nxt) < 0 {
		return true
	}

	n := len(kcp.rcv_buf) - 1
	insert_idx := 0 //找到在rev_buf中待插入的位置
	repeat := false
	for i := len(kcp.rcv_buf) - 1; i >= 0; i-- {
		seg := &kcp.rcv_buf[i]
		if seg.sn == sn {
			repeat = true
			break
		}
		//sn>seg.sn
		if util.KCP_itimediff(sn, seg.sn) > 0 {
			insert_idx = i + 1
			break
		}
	}

	if !repeat {
		// replicate the content if it's new
		dataCopy := util.XmitBuf.Get().([]byte)[:len(newseg.data)] //将mtulimit buf中的数据转存到xmitBuf缓存区中
		copy(dataCopy, newseg.data)
		newseg.data = dataCopy

		if insert_idx == n+1 {
			kcp.rcv_buf = append(kcp.rcv_buf, newseg)
		} else {
			kcp.rcv_buf = append(kcp.rcv_buf, segment{})
			copy(kcp.rcv_buf[insert_idx+1:], kcp.rcv_buf[insert_idx:])
			kcp.rcv_buf[insert_idx] = newseg
		}
	}

	// move available data from rcv_buf -> rcv_queue
	count := 0
	for k := range kcp.rcv_buf {
		seg := &kcp.rcv_buf[k]
		if seg.sn == kcp.rcv_nxt && len(kcp.rcv_queue)+count < int(kcp.rcv_wnd) {
			kcp.rcv_nxt++
			count++
		} else {
			break
		}
	}
	if count > 0 {
		kcp.rcv_queue = append(kcp.rcv_queue, kcp.rcv_buf[:count]...)
		kcp.rcv_buf = kcp.remove_front(kcp.rcv_buf, count)
	}

	return repeat
}

// Input a packet into kcp state machine.
//
// 'regular' indicates it's a real data packet from remote, and it means it's not generated from ReedSolomon
// codecs.
//
// 'ackNoDelay' will trigger immediate ACK, but surely it will not be efficient in bandwidth
//func (kcp *KCP) Input(data []byte, regular, ackNoDelay bool) int {

func (kcp *KCP) Input(data []byte) int {
	snd_una := kcp.snd_una
	if len(data) < IKCP_OVERHEAD { //长度小于24B，放弃decode
		return -1
	}

	var latest uint32 // the latest ack packet
	var flag int      //标识是否收到了ack包
	//var inSegs uint64
	var windowSlides bool

	for {
		var ts, sn, length, una, conv uint32
		var wnd uint16
		var cmd, frg uint8

		if len(data) < IKCP_OVERHEAD { //长度小于24B，放弃decode
			break
		}

		data = util.Ikcp_decode32u(data, &conv)
		//从精简版流程来看，如果是server端，这个判断一定是true
		//如果是client端，不一定
		if conv != kcp.Conv {
			return -1
		}
		data = util.Ikcp_decode8u(data, &cmd)
		data = util.Ikcp_decode8u(data, &frg)
		data = util.Ikcp_decode16u(data, &wnd)
		data = util.Ikcp_decode32u(data, &ts)
		data = util.Ikcp_decode32u(data, &sn)
		data = util.Ikcp_decode32u(data, &una)
		data = util.Ikcp_decode32u(data, &length)
		if len(data) < int(length) {
			return -2
		}

		if cmd != IKCP_CMD_PUSH && cmd != IKCP_CMD_ACK &&
			cmd != IKCP_CMD_WASK && cmd != IKCP_CMD_WINS {
			return -3
		}

		// only trust window updates from regular packets. i.e: latest update
		//if regular {
		kcp.Rmt_wnd = uint32(wnd) //从对端接收到的wnd信息，应该是remote_wnd
		//}
		if kcp.parse_una(una) > 0 {
			windowSlides = true
		}
		kcp.shrink_buf() //这里可能会更新kcp.snd_una

		if cmd == IKCP_CMD_ACK { //对端发过来的ack包
			kcp.parse_ack(sn)
			kcp.parse_fastack(sn, ts)
			flag |= 1 //标识是否收到了ack包
			latest = ts
		} else if cmd == IKCP_CMD_PUSH { //对端发过来的数据包
			//repeat := true
			//sn<kcp.rcv_nxt+kcp.rcv_wnd，接收到的数据，在接收窗口内
			if util.KCP_itimediff(sn, kcp.rcv_nxt+kcp.rcv_wnd) < 0 {
				kcp.ack_push(sn, ts) //准备ack
				//sn>=kcp.rev_nxt，在接收窗口区间
				if util.KCP_itimediff(sn, kcp.rcv_nxt) >= 0 {
					var seg segment
					seg.conv = conv
					seg.cmd = cmd
					seg.frg = frg
					seg.wnd = wnd
					seg.ts = ts
					seg.sn = sn
					seg.una = una
					//data自带length信息
					seg.data = data[:length] // delayed data copying
					//repeat = kcp.parse_data(seg)
					kcp.parse_data(seg)
				}
			}
			//if regular && repeat {
			//	atomic.AddUint64(&DefaultSnmp.RepeatSegs, 1)
			//}
		} else if cmd == IKCP_CMD_WASK { //对端在问本端的窗口大小
			// ready to send back IKCP_CMD_WINS in Ikcp_flush
			// tell remote my window size
			kcp.probe |= IKCP_ASK_TELL
		} else if cmd == IKCP_CMD_WINS {
			// do nothing
		} else {
			return -3
		}

		//inSegs++
		data = data[length:]
	}
	//atomic.AddUint64(&DefaultSnmp.InSegs, inSegs)

	// update rtt with the latest ts
	//收到了ack包，就可以更新rtt了
	// ignore the FEC packet
	//if flag != 0 && regular {
	if flag != 0 {
		current := util.CurrentMs()
		//current >= latest
		if util.KCP_itimediff(current, latest) >= 0 {
			kcp.update_rto(util.KCP_itimediff(current, latest))
		}
	}

	//// cwnd update when packet arrived
	//if kcp.nocwnd == 0 {
	//新的snd_una比旧的snd_una大，说明收到合法的数据包了
	// kcp.snd_una>snd_una
	if util.KCP_itimediff(kcp.snd_una, snd_una) > 0 {
		if kcp.cwnd < kcp.Rmt_wnd { //如果本端cwnd比对端rmt_wnd小
			//mss := uint32(IKCP_MSS)
			if kcp.cwnd < kcp.ssthresh {
				kcp.cwnd++
				kcp.incr += IKCP_MSS
			} else {
				if kcp.incr < IKCP_MSS {
					kcp.incr = IKCP_MSS
				}
				kcp.incr += (IKCP_MSS*IKCP_MSS)/kcp.incr + (IKCP_MSS / 16)
				if (kcp.cwnd+1)*IKCP_MSS <= kcp.incr {
					//if IKCP_MSS > 0 {
					kcp.cwnd = (kcp.incr + IKCP_MSS - 1) / IKCP_MSS
					//} else {
					//	kcp.cwnd = kcp.incr + mss - 1
					//}
				}
			}
			if kcp.cwnd > kcp.Rmt_wnd {
				kcp.cwnd = kcp.Rmt_wnd
				kcp.incr = kcp.Rmt_wnd * IKCP_MSS
			}
		}
	}
	//}

	if windowSlides { // if window has slided, flush
		//kcp.flush(false)
		kcp.Flush()
		//} else if ackNoDelay && len(kcp.acklist) > 0 { // ack immediately
		//	kcp.flush(true)
	}
	return 0
}

func (kcp *KCP) wnd_unused() uint16 {
	if len(kcp.rcv_queue) < int(kcp.rcv_wnd) {
		return uint16(int(kcp.rcv_wnd) - len(kcp.rcv_queue))
	}
	return 0
}

// flush pending data
func (kcp *KCP) Flush() uint32 {
	var seg segment
	seg.conv = kcp.Conv
	seg.cmd = IKCP_CMD_ACK
	seg.wnd = kcp.wnd_unused()
	seg.una = kcp.rcv_nxt

	buffer := kcp.buffer
	//ptr := buffer[kcp.reserved:] // keep n bytes untouched
	ptr := buffer // keep n bytes untouched

	// makeSpace makes room for writing
	makeSpace := func(space int) {
		size := len(buffer) - len(ptr)
		if size+space > IKCP_MTU_DEF {
			//kcp.output(buffer, size)
			kcp.sessionI.Output(buffer, size)
			//ptr = buffer[kcp.reserved:]
			ptr = buffer //写出之后，重新归0
		}
	}

	// flush bytes in buffer if there is any
	flushBuffer := func() {
		size := len(buffer) - len(ptr)
		//if size > kcp.reserved {
		if size > 0 {
			//kcp.output(buffer, size)
			kcp.sessionI.Output(buffer, size)
		}
	}

	// flush acknowledges
	for i, ack := range kcp.acklist {
		makeSpace(IKCP_OVERHEAD)
		// filter jitters caused by bufferbloat
		if util.KCP_itimediff(ack.sn, kcp.rcv_nxt) >= 0 || len(kcp.acklist)-1 == i { //第二个条件是什么意思
			seg.sn, seg.ts = ack.sn, ack.ts
			ptr = seg.encode(ptr)
		}
	}
	kcp.acklist = kcp.acklist[0:0]

	//if ackOnly { // flash remain ack segments
	//	flushBuffer()
	//	return kcp.interval
	//}

	// probe window size (if remote window size equals zero)
	if kcp.Rmt_wnd == 0 {
		current := util.CurrentMs()
		if kcp.probe_wait == 0 {
			kcp.probe_wait = IKCP_PROBE_INIT
			kcp.ts_probe = current + kcp.probe_wait
		} else {
			if util.KCP_itimediff(current, kcp.ts_probe) >= 0 {
				if kcp.probe_wait < IKCP_PROBE_INIT {
					kcp.probe_wait = IKCP_PROBE_INIT
				}
				kcp.probe_wait += kcp.probe_wait / 2
				if kcp.probe_wait > IKCP_PROBE_LIMIT {
					kcp.probe_wait = IKCP_PROBE_LIMIT
				}
				kcp.ts_probe = current + kcp.probe_wait
				kcp.probe |= IKCP_ASK_SEND
			}
		}
	} else {
		kcp.ts_probe = 0
		kcp.probe_wait = 0
	}

	// flush window probing commands
	if (kcp.probe & IKCP_ASK_SEND) != 0 {
		seg.cmd = IKCP_CMD_WASK
		makeSpace(IKCP_OVERHEAD)
		ptr = seg.encode(ptr)
	}

	// flush window probing commands
	if (kcp.probe & IKCP_ASK_TELL) != 0 {
		seg.cmd = IKCP_CMD_WINS
		makeSpace(IKCP_OVERHEAD)
		ptr = seg.encode(ptr)
	}

	kcp.probe = 0

	// calculate window size
	cwnd := util.KCP_imin_(IKCP_WND_SND, kcp.Rmt_wnd)
	//if kcp.nocwnd == 0 {
	cwnd = util.KCP_imin_(kcp.cwnd, cwnd)
	//}

	// sliding window, controlled by snd_nxt && sna_una+cwnd
	newSegsCount := 0
	for k := range kcp.snd_queue {
		if util.KCP_itimediff(kcp.snd_nxt, kcp.snd_una+cwnd) >= 0 {
			break
		}
		newseg := kcp.snd_queue[k]
		newseg.conv = kcp.Conv
		newseg.cmd = IKCP_CMD_PUSH
		newseg.sn = kcp.snd_nxt
		kcp.snd_buf = append(kcp.snd_buf, newseg)
		kcp.snd_nxt++
		newSegsCount++
	}
	if newSegsCount > 0 {
		kcp.snd_queue = kcp.remove_front(kcp.snd_queue, newSegsCount)
	}

	// calculate resent
	//resent := uint32(kcp.fastresend)
	//if kcp.fastresend <= 0 {
	//	resent = 0xffffffff
	//}
	var resent uint32 = 0xffffffff

	// check for retransmissions
	current := util.CurrentMs()
	var change, lostSegs, earlyRetransSegs uint64
	minrto := int32(IKCP_INTERVAL)

	ref := kcp.snd_buf[:len(kcp.snd_buf)] // for bounds check elimination
	for k := range ref {
		segment := &ref[k]
		needsend := false
		if segment.acked == 1 {
			continue
		}
		if segment.xmit == 0 { // initial transmit
			needsend = true
			segment.rto = kcp.rx_rto
			segment.resendts = current + segment.rto
			//} else if segment.fastack >= resent { // fast retransmit
			//	needsend = true
			//	segment.fastack = 0
			//	segment.rto = kcp.rx_rto
			//	segment.resendts = current + segment.rto
			//	change++
			//	fastRetransSegs++
		} else if segment.fastack > 0 && newSegsCount == 0 { // early retransmit
			needsend = true
			segment.fastack = 0
			segment.rto = kcp.rx_rto
			segment.resendts = current + segment.rto
			change++
			earlyRetransSegs++
		} else if util.KCP_itimediff(current, segment.resendts) >= 0 { // RTO
			needsend = true
			//if kcp.nodelay == 0 {
			//	segment.rto += kcp.rx_rto
			//} else {
			//	segment.rto += kcp.rx_rto / 2
			//}
			segment.rto += kcp.rx_rto
			segment.fastack = 0
			segment.resendts = current + segment.rto
			lostSegs++
		}

		if needsend {
			current = util.CurrentMs()
			segment.xmit++
			segment.ts = current
			segment.wnd = seg.wnd
			segment.una = seg.una

			need := IKCP_OVERHEAD + len(segment.data)
			makeSpace(need)
			ptr = segment.encode(ptr)
			copy(ptr, segment.data)
			ptr = ptr[len(segment.data):]

			//if segment.xmit >= IKCP_DEADLINK {
			//	kcp.state = 0xFFFFFFFF
			//}
		}

		// get the nearest rto
		if rto := util.KCP_itimediff(segment.resendts, current); rto > 0 && rto < minrto {
			minrto = rto
		}
	}

	// flash remain segments
	flushBuffer()

	//// counter updates
	//sum := lostSegs
	//if lostSegs > 0 {
	//	atomic.AddUint64(&DefaultSnmp.LostSegs, lostSegs)
	//}
	//if fastRetransSegs > 0 {
	//	atomic.AddUint64(&DefaultSnmp.FastRetransSegs, fastRetransSegs)
	//	sum += fastRetransSegs
	//}
	//if earlyRetransSegs > 0 {
	//	atomic.AddUint64(&DefaultSnmp.EarlyRetransSegs, earlyRetransSegs)
	//	sum += earlyRetransSegs
	//}
	//if sum > 0 {
	//	atomic.AddUint64(&DefaultSnmp.RetransSegs, sum)
	//}

	// cwnd update
	//if kcp.nocwnd == 0 {
	// update ssthresh
	// rate halving, https://tools.ietf.org/html/rfc6937
	if change > 0 {
		inflight := kcp.snd_nxt - kcp.snd_una
		kcp.ssthresh = inflight / 2
		if kcp.ssthresh < IKCP_THRESH_MIN {
			kcp.ssthresh = IKCP_THRESH_MIN
		}
		kcp.cwnd = kcp.ssthresh + resent
		kcp.incr = kcp.cwnd * IKCP_MSS
	}

	// congestion control, https://tools.ietf.org/html/rfc5681
	if lostSegs > 0 {
		kcp.ssthresh = cwnd / 2
		if kcp.ssthresh < IKCP_THRESH_MIN {
			kcp.ssthresh = IKCP_THRESH_MIN
		}
		kcp.cwnd = 1
		kcp.incr = IKCP_MSS
	}

	if kcp.cwnd < 1 {
		kcp.cwnd = 1
		kcp.incr = IKCP_MSS
	}
	//}

	return uint32(minrto)
}

//// (deprecated)
////
//// Update updates state (call it repeatedly, every 10ms-100ms), or you can ask
//// ikcp_check when to call it again (without ikcp_input/_send calling).
//// 'current' - current timestamp in millisec.
//func (kcp *KCP) Update() {
//	var slap int32
//
//	current := currentMs()
//	if kcp.updated == 0 {
//		kcp.updated = 1
//		kcp.ts_flush = current
//	}
//
//	slap = _itimediff(current, kcp.ts_flush)
//
//	if slap >= 10000 || slap < -10000 {
//		kcp.ts_flush = current
//		slap = 0
//	}
//
//	if slap >= 0 {
//		kcp.ts_flush += IKCP_INTERVAL
//		if _itimediff(current, kcp.ts_flush) >= 0 {
//			kcp.ts_flush = current + IKCP_INTERVAL
//		}
//		//kcp.flush(false)
//		kcp.flush()
//	}
//}

//// (deprecated)
////
//// Check determines when should you invoke ikcp_update:
//// returns when you should invoke ikcp_update in millisec, if there
//// is no ikcp_input/_send calling. you can call ikcp_update in that
//// time, instead of call update repeatly.
//// Important to reduce unnacessary ikcp_update invoking. use it to
//// schedule ikcp_update (eg. implementing an epoll-like mechanism,
//// or optimize ikcp_update when handling massive kcp connections)
//func (kcp *KCP) Check() uint32 {
//	current := currentMs()
//	ts_flush := kcp.ts_flush
//	tm_flush := int32(0x7fffffff)
//	tm_packet := int32(0x7fffffff)
//	minimal := uint32(0)
//	if kcp.updated == 0 {
//		return current
//	}
//
//	if _itimediff(current, ts_flush) >= 10000 ||
//		_itimediff(current, ts_flush) < -10000 {
//		ts_flush = current
//	}
//
//	if _itimediff(current, ts_flush) >= 0 {
//		return current
//	}
//
//	tm_flush = _itimediff(ts_flush, current)
//
//	for k := range kcp.snd_buf {
//		seg := &kcp.snd_buf[k]
//		diff := _itimediff(seg.resendts, current)
//		if diff <= 0 {
//			return current
//		}
//		if diff < tm_packet {
//			tm_packet = diff
//		}
//	}
//
//	minimal = uint32(tm_packet)
//	if tm_packet >= tm_flush {
//		minimal = uint32(tm_flush)
//	}
//	if minimal >= IKCP_INTERVAL {
//		minimal = IKCP_INTERVAL
//	}
//
//	return current + minimal
//}

//// SetMtu changes MTU size, default is 1400
//func (kcp *KCP) SetMtu(mtu int) int {
//	if mtu < 50 || mtu < IKCP_OVERHEAD {
//		return -1
//	}
//	if kcp.reserved >= int(kcp.mtu-IKCP_OVERHEAD) || kcp.reserved < 0 {
//		return -1
//	}
//
//	buffer := make([]byte, mtu)
//	if buffer == nil {
//		return -2
//	}
//	kcp.mtu = uint32(mtu)
//	kcp.mss = kcp.mtu - IKCP_OVERHEAD - uint32(kcp.reserved)
//	kcp.buffer = buffer
//	return 0
//}

//// NoDelay options
//// fastest: ikcp_nodelay(kcp, 1, 20, 2, 1)
//// nodelay: 0:disable(default), 1:enable
//// interval: internal update timer interval in millisec, default is 100ms
//// resend: 0:disable fast resend(default), 1:enable fast resend
//// nc: 0:normal congestion control(default), 1:disable congestion control
//// 这个函数暂时没有调用者
//func (kcp *KCP) NoDelay(nodelay, interval, resend, nc int) int {
//	if nodelay >= 0 {
//		kcp.nodelay = uint32(nodelay)
//		if nodelay != 0 {
//			kcp.rx_minrto = IKCP_RTO_NODELAY
//		} else {
//			kcp.rx_minrto = IKCP_RTO_MIN
//		}
//	}
//	if interval >= 0 {
//		if interval > 5000 {
//			interval = 5000
//		} else if interval < 10 {
//			interval = 10
//		}
//		kcp.interval = uint32(interval)
//	}
//	if resend >= 0 {
//		kcp.fastresend = int32(resend)
//	}
//	if nc >= 0 {
//		kcp.nocwnd = int32(nc)
//	}
//	return 0
//}

//// WndSize sets maximum window size: sndwnd=32, rcvwnd=32 by default
//func (kcp *KCP) WndSize(sndwnd, rcvwnd int) int {
//	if sndwnd > 0 {
//		kcp.snd_wnd = uint32(sndwnd)
//	}
//	if rcvwnd > 0 {
//		kcp.rcv_wnd = uint32(rcvwnd)
//	}
//	return 0
//}

// WaitSnd gets how many packet is waiting to be sent
func (kcp *KCP) WaitSnd() int {
	return len(kcp.snd_buf) + len(kcp.snd_queue)
}

// remove front n elements from queue
// if the number of elements to remove is more than half of the size.
// just shift the rear elements to front, otherwise just reslice q to q[n:]
// then the cost of runtime.growslice can always be less than n/2
func (kcp *KCP) remove_front(q []segment, n int) []segment {
	if n > cap(q)/2 {
		newn := copy(q, q[n:])
		return q[:newn]
	}
	return q[n:]
}

// Release all cached outgoing segments
func (kcp *KCP) ReleaseTX() {
	for k := range kcp.snd_queue {
		if kcp.snd_queue[k].data != nil {
			util.XmitBuf.Put(kcp.snd_queue[k].data)
		}
	}
	for k := range kcp.snd_buf {
		if kcp.snd_buf[k].data != nil {
			util.XmitBuf.Put(kcp.snd_buf[k].data)
		}
	}
	kcp.snd_queue = nil
	kcp.snd_buf = nil
}
