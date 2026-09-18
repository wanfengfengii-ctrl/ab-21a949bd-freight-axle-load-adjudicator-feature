// Package bridge 实现临时便桥桥面承载窗口分析：车辆沿车头方向平移通过桥面时，
// 扫描各轴进入与离开有效桥面的事件，求任一时刻落在有效桥面的轴载合计最大值，
// 并给出通行或拦停结论。本包为独立领域逻辑，不调用轴组裁决与重测比对。
//
// 轴位置与轴载荷只设下限（位置非负、载荷为正），不设上限：落桥载荷按任意精度
// 整数求和，离开事件位移的排序对超出 int 范围的情形做了保序处理，极大数值
// 不会溢出，最大桥面载荷不会被报成零或少算。
package bridge

import (
	"fmt"
	"math"
	"math/big"
	"sort"
)

// 输入约束常量，单位见字段名。
const (
	MinAxles          = 1
	MaxAxles          = 12
	MinAxleLoadKg     = 1
	MinPositionMm     = 0
	MinBridgeLengthMm = 1000
	MaxBridgeLengthMm = 50000
	MinApprovedLoadKg = 1
	MaxApprovedLoadKg = 200000
	MinSpeedMmPerS    = 1
	MaxSpeedMmPerS    = 50000
)

// 分析结论：最大桥面载荷不超过核定载荷（含相等）时通行，否则拦停。
const (
	ConclusionPass = "通行"
	ConclusionStop = "拦停"
)

// Input 为一次桥面承载窗口分析的输入：轴位置按车头方向严格递增（车尾轴在前、
// 车头轴在后），载荷与轴一一对应，桥长为桥面有效长度，核定载荷为现场核定值。
// SpeedMmPerS 选填：车辆匀速通过桥面的预计车速（毫米每秒）。提供时额外计算
// 超载区段与累计时长；为 nil 时不计算，分析行为与引入该能力前完全一致。
type Input struct {
	AxlePositionsMm []int
	AxleLoadsKg     []int
	BridgeLengthMm  int
	ApprovedLoadKg  int
	SpeedMmPerS     *int
}

// Window 为领域对象“连续落桥轴区间”：车辆平移过程中，落在有效桥面的轴始终
// 构成一段连续区间，本结构记录该区间形成时的首尾轴序号（1 基，按提交顺序）、
// 当时的车辆位移与落桥轴载合计（任意精度整数，极大载荷不溢出）。
type Window struct {
	FirstAxle      int
	LastAxle       int
	DisplacementMm int
	LoadKg         *big.Int
}

// OverloadSegment 为一段载荷恒定且超过核定载荷的位移区段：车辆以匀速通过时，
// 该区段内桥面载荷保持 LoadKg 不变。区段由相邻事件位移界定，位移长度恒大于零；
// 相邻且载荷相同的区段已先行合并。时长按“位移差×1000÷车速”向上取整，
// 以任意精度整数表示，极大位移不会溢出。
type OverloadSegment struct {
	StartDisplacementMm int      // 区段起始车辆位移，毫米
	EndDisplacementMm   int      // 区段结束车辆位移，毫米
	LoadKg              *big.Int // 区段内的恒定载荷，千克
	DurationMs          *big.Int // 以给定车速通过该区段的时长，毫秒（向上取整）
}

// Analysis 为桥面承载窗口分析结果：整个平移过程中桥面载荷的最大值、
// 取得最大值时的首尾轴序号与车辆位移，以及通行或拦停结论。
// 提供车速时额外给出超载区段清单与累计超载时长；未提供车速时两者为 nil。
type Analysis struct {
	MaxLoadKg      *big.Int // 最大桥面载荷，任意精度整数，千克
	FirstAxle      int      // 取得最大值时的首轴序号（1 基）
	LastAxle       int      // 取得最大值时的尾轴序号（1 基）
	DisplacementMm int      // 取得最大值时的车辆位移，毫米
	Conclusion     string   // ConclusionPass 或 ConclusionStop
	// OverloadSegments 为超载区段清单，按起始位移升序；仅在提供车速时计算，
	// 无超载区段时为空切片而非 nil。同一位移处的瞬时超载不进入区段。
	OverloadSegments []OverloadSegment
	// OverloadDurationMs 为各区段时长的累计值，毫秒；仅在提供车速时非 nil。
	OverloadDurationMs *big.Int
}

// Validate 仅校验输入合法性，不产出分析结果。
func Validate(in Input) error {
	n := len(in.AxleLoadsKg)
	if n < MinAxles || n > MaxAxles {
		return fmt.Errorf("轴数须为 %d 至 %d，实际为 %d", MinAxles, MaxAxles, n)
	}
	if len(in.AxlePositionsMm) != n {
		return fmt.Errorf("轴位置项数必须与轴数一致：轴数 %d 时应为 %d，实际为 %d",
			n, n, len(in.AxlePositionsMm))
	}
	for i, load := range in.AxleLoadsKg {
		if load < MinAxleLoadKg {
			return fmt.Errorf("第 %d 轴载荷 %d 必须不小于 %d 千克",
				i+1, load, MinAxleLoadKg)
		}
	}
	for i, pos := range in.AxlePositionsMm {
		if pos < MinPositionMm {
			return fmt.Errorf("第 %d 轴位置 %d 不得为负（毫米）", i+1, pos)
		}
		if i > 0 && pos <= in.AxlePositionsMm[i-1] {
			if pos == in.AxlePositionsMm[i-1] {
				return fmt.Errorf("第 %d 轴与第 %d 轴位置重复（均为 %d 毫米），轴位置不得重复",
					i, i+1, pos)
			}
			return fmt.Errorf("轴位置须按车头方向严格递增：第 %d 轴位置 %d 不大于第 %d 轴位置 %d（毫米）",
				i+1, pos, i, in.AxlePositionsMm[i-1])
		}
	}
	if in.BridgeLengthMm < MinBridgeLengthMm || in.BridgeLengthMm > MaxBridgeLengthMm {
		return fmt.Errorf("桥面有效长度 %d 超出允许范围 %d-%d 毫米",
			in.BridgeLengthMm, MinBridgeLengthMm, MaxBridgeLengthMm)
	}
	if in.ApprovedLoadKg < MinApprovedLoadKg || in.ApprovedLoadKg > MaxApprovedLoadKg {
		return fmt.Errorf("核定载荷 %d 超出允许范围 %d-%d 千克",
			in.ApprovedLoadKg, MinApprovedLoadKg, MaxApprovedLoadKg)
	}
	if in.SpeedMmPerS != nil &&
		(*in.SpeedMmPerS < MinSpeedMmPerS || *in.SpeedMmPerS > MaxSpeedMmPerS) {
		return fmt.Errorf("车速 %d 超出允许范围 %d-%d 毫米每秒",
			*in.SpeedMmPerS, MinSpeedMmPerS, MaxSpeedMmPerS)
	}
	return nil
}

// Analyze 校验并执行桥面承载窗口分析。任何输入非法都返回 error，
// 调用方必须整体拒绝，不得使用部分结果。
func Analyze(in Input) (*Analysis, error) {
	if err := Validate(in); err != nil {
		return nil, err
	}
	windows := ScanWindows(in)
	best := windows[0]
	for _, w := range windows[1:] {
		if preferred(w, best) {
			best = w
		}
	}
	conclusion := ConclusionPass
	if best.LoadKg.Cmp(big.NewInt(int64(in.ApprovedLoadKg))) > 0 {
		conclusion = ConclusionStop
	}
	analysis := &Analysis{
		MaxLoadKg:      best.LoadKg,
		FirstAxle:      best.FirstAxle,
		LastAxle:       best.LastAxle,
		DisplacementMm: best.DisplacementMm,
		Conclusion:     conclusion,
	}
	if in.SpeedMmPerS != nil {
		analysis.OverloadSegments, analysis.OverloadDurationMs = overloadExposure(in, *in.SpeedMmPerS)
	}
	return analysis, nil
}

// axleEvent 为一根轴进入或离开有效桥面的事件。
type axleEvent struct {
	displacementMm int  // 事件发生时的车辆位移，毫米（可表示时）
	axle           int  // 轴下标（0 基，按提交顺序）
	enter          bool // true 进入、false 离开
	// beyondInt 仅用于离开事件：离开位移 = 进入位移 + 桥长 超出 int 范围时
	// 为 true，此时 displacementMm 存进入位移。数学上该离开位移大于任何
	// 可表示位移，排序时置于全部可表示事件之后；溢出的离开事件之间按
	// 进入位移排序（同加桥长不改变相对次序），全程无需计算溢出值。
	beyondInt bool
}

// scanEvents 生成车辆平移过程中的全部轴进入与离开事件，并按位移升序排序。
//
// 位移原点为车头轴抵达桥入口（桥面坐标 0）的时刻，此后车辆每前进 1 毫米
// 位移加 1；轴 i 的桥面坐标 = 位移 − (车头轴位置 − 轴 i 位置)，坐标落在
// [0, 桥长] 闭区间内即视为落桥——桥面边界恰好容纳的轴（坐标 0 或桥长处）
// 同样计入载荷。同一位移处进入事件先于离开事件：前轴恰抵桥出口、后轴恰抵
// 桥入口的瞬间，两根边界轴都落在闭区间桥面上，须在同一时刻一并计入载荷。
// 位移超出 int 范围的离开事件排在全部可表示事件之后（相互之间按进入位移
// 排序，同加桥长不改变相对次序），全程无需计算溢出值。调用前必须完成 Validate。
func scanEvents(in Input) []axleEvent {
	n := len(in.AxlePositionsMm)
	head := in.AxlePositionsMm[n-1] // 车头轴位置（最大）
	events := make([]axleEvent, 0, 2*n)
	for i, pos := range in.AxlePositionsMm {
		enter := head - pos // 轴 i 抵达桥入口时的车辆位移；head >= pos，不会下溢
		events = append(events, axleEvent{displacementMm: enter, axle: i, enter: true})
		leave := axleEvent{axle: i}
		if enter <= math.MaxInt-in.BridgeLengthMm {
			leave.displacementMm = enter + in.BridgeLengthMm
		} else {
			leave.displacementMm = enter
			leave.beyondInt = true
		}
		events = append(events, leave)
	}
	sort.Slice(events, func(a, b int) bool {
		x, y := events[a], events[b]
		if x.beyondInt != y.beyondInt {
			return !x.beyondInt // 可表示事件在前，溢出离开事件排在最后
		}
		if x.displacementMm != y.displacementMm {
			return x.displacementMm < y.displacementMm
		}
		return x.enter && !y.enter
	})
	return events
}

// ScanWindows 扫描车辆平移过程中的全部进入与离开事件，按位移升序给出每个
// 事件发生后形成的连续落桥轴区间（空区间不产出）。
//
// 位移原点与落桥判定见 scanEvents。调用前必须完成 Validate。
func ScanWindows(in Input) []Window {
	n := len(in.AxlePositionsMm)
	events := scanEvents(in)

	// 进入与离开事件均按轴序号从大到小到达：进入的新轴接在区间首端，
	// 离开的轴从区间尾端收缩，落桥轴因此始终保持连续区间。
	// 载荷按任意精度整数累计，极大载荷求和不会溢出。
	windows := make([]Window, 0, len(events))
	first, last := n, n-1 // first > last 表示空区间
	load := new(big.Int)
	for _, ev := range events {
		if ev.enter {
			first = ev.axle
			load.Add(load, big.NewInt(int64(in.AxleLoadsKg[ev.axle])))
		} else {
			last = ev.axle - 1
			load.Sub(load, big.NewInt(int64(in.AxleLoadsKg[ev.axle])))
		}
		if first > last {
			continue // 空区间不产出
		}
		if ev.beyondInt {
			// 位移超出 int 范围的离开事件不产出快照：所有进入事件都排在它
			// 之前，此后只剩离开事件，区间载荷严格单调下降，被跳过区间的
			// 载荷必小于收缩前已记录的状态，不可能成为峰值；其位移本身也
			// 无法用 int 表示。
			continue
		}
		windows = append(windows, Window{
			FirstAxle:      first + 1,
			LastAxle:       last + 1,
			DisplacementMm: ev.displacementMm,
			LoadKg:         new(big.Int).Set(load),
		})
	}
	return windows
}

// overloadExposure 复用轴进入与离开事件，把相邻事件位移之间载荷恒定的区间
// 生成候选区段，筛出位移长度大于零且载荷超过核定值的区段；相邻且载荷相同的
// 区段先行合并，再按“位移差×1000÷车速”向上取整给出各区段毫秒数，并累计总时长。
//
// 同一位移处连续事件之间的瞬时状态（位移长度为零）不进入区段与累计时长，
// 但仍通过 ScanWindows 参与原峰值裁决；位移超出 int 范围的离开事件不产出
// 区段（其真实位移无法用 int 表示，与溢出事件不产出窗口快照同理）。
// 时长以任意精度整数计算，极大位移与极小车速的组合不会溢出。
// 调用前必须完成 Validate，且 speed 已在允许范围内。
func overloadExposure(in Input, speed int) ([]OverloadSegment, *big.Int) {
	n := len(in.AxlePositionsMm)
	events := scanEvents(in)
	approved := big.NewInt(int64(in.ApprovedLoadKg))

	// 与 ScanWindows 相同地推进落桥轴区间：每个事件处理完后，到下一事件
	// 位移之前载荷保持恒定，构成一个候选区段。
	candidates := make([]OverloadSegment, 0, len(events))
	first, last := n, n-1 // first > last 表示空区间
	load := new(big.Int)
	for i, ev := range events {
		if ev.enter {
			first = ev.axle
			load.Add(load, big.NewInt(int64(in.AxleLoadsKg[ev.axle])))
		} else {
			last = ev.axle - 1
			load.Sub(load, big.NewInt(int64(in.AxleLoadsKg[ev.axle])))
		}
		if i+1 >= len(events) || ev.beyondInt || events[i+1].beyondInt {
			continue // 最后一个事件之后、以及涉及溢出位移的区间不产出
		}
		start, end := ev.displacementMm, events[i+1].displacementMm
		if end <= start {
			continue // 同一位移处的瞬时状态位移长度为零，不进入区段
		}
		if first > last || load.Cmp(approved) <= 0 {
			continue // 空区间与未超载区段不产出
		}
		candidates = append(candidates, OverloadSegment{
			StartDisplacementMm: start,
			EndDisplacementMm:   end,
			LoadKg:              new(big.Int).Set(load),
		})
	}

	// 相邻且载荷相同的区段合并：同一位移处的瞬时状态被筛除后，其两侧
	// 载荷相同的区段变为相邻，应并为一段（时长按合并后的位移差计算）。
	merged := make([]OverloadSegment, 0, len(candidates))
	for _, seg := range candidates {
		if m := len(merged); m > 0 &&
			merged[m-1].EndDisplacementMm == seg.StartDisplacementMm &&
			merged[m-1].LoadKg.Cmp(seg.LoadKg) == 0 {
			merged[m-1].EndDisplacementMm = seg.EndDisplacementMm
			continue
		}
		merged = append(merged, seg)
	}

	// 各区段时长 = ceil(位移差×1000÷车速)，全程任意精度整数；累计为各区段之和。
	total := new(big.Int)
	thousand := big.NewInt(1000)
	speedBig := big.NewInt(int64(speed))
	speedMinusOne := big.NewInt(int64(speed - 1))
	for i := range merged {
		ms := big.NewInt(int64(merged[i].EndDisplacementMm - merged[i].StartDisplacementMm))
		ms.Mul(ms, thousand)
		ms.Add(ms, speedMinusOne) // 正整数向上取整：(x + 除数 - 1) ÷ 除数
		ms.Div(ms, speedBig)
		merged[i].DurationMs = ms
		total.Add(total, ms)
	}
	return merged, total
}

// preferred 判定候选区间 a 是否优于 b：载荷更大者优先；载荷相同取车辆位移
// 更小者；位移也相同取首轴序号更小者。该次序保证相同最大值出现多次时
// 仍能确定唯一结果。
func preferred(a, b Window) bool {
	if c := a.LoadKg.Cmp(b.LoadKg); c != 0 {
		return c > 0
	}
	if a.DisplacementMm != b.DisplacementMm {
		return a.DisplacementMm < b.DisplacementMm
	}
	return a.FirstAxle < b.FirstAxle
}
