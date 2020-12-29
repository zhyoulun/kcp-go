package kcp

import "github.com/zhyoulun/kcp-go/src/util"

// segment defines a KCP segment
type segment struct {
	conv     uint32
	cmd      uint8
	frg      uint8
	wnd      uint16
	ts       uint32
	sn       uint32
	una      uint32
	rto      uint32
	xmit     uint32
	resendts uint32
	fastack  uint32
	acked    uint32 // mark if the seg has acked
	data     []byte
}

// encode a segment into buffer
func (seg *segment) encode(ptr []byte) []byte {
	ptr = util.Ikcp_encode32u(ptr, seg.conv)
	ptr = util.Ikcp_encode8u(ptr, seg.cmd)
	ptr = util.Ikcp_encode8u(ptr, seg.frg)
	ptr = util.Ikcp_encode16u(ptr, seg.wnd)
	ptr = util.Ikcp_encode32u(ptr, seg.ts)
	ptr = util.Ikcp_encode32u(ptr, seg.sn)
	ptr = util.Ikcp_encode32u(ptr, seg.una)
	ptr = util.Ikcp_encode32u(ptr, uint32(len(seg.data)))
	//atomic.AddUint64(&DefaultSnmp.OutSegs, 1)
	return ptr
}
