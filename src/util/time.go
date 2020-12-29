package util

import "time"

// monotonic reference time point
var refTime = time.Now()

// currentMs returns current elapsed monotonic milliseconds since program startup
// 程序启动后，距离当前的毫秒数
// uint32, 0到4,294,967,295，   4294967/3600/24=49天一个轮回
func CurrentMs() uint32 { return uint32(time.Since(refTime) / time.Millisecond) }

//求最小值
func KCP_imin_(a, b uint32) uint32 {
	if a <= b {
		return a
	}
	return b
}

//求最大值
func KCP_imax_(a, b uint32) uint32 {
	if a >= b {
		return a
	}
	return b
}

func KCP_ibound_(lower, middle, upper uint32) uint32 {
	return KCP_imin_(KCP_imax_(lower, middle), upper)
}

func KCP_itimediff(later, earlier uint32) int32 {
	return (int32)(later - earlier)
}
