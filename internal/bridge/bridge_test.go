package bridge

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertAnalysis 校验一次分析结果：最大载荷按字符串比较（任意精度），
// 其余字段逐项相等。
func assertAnalysis(t *testing.T, wantMaxLoadKg string, wantFirst, wantLast, wantDisplacement int,
	wantConclusion string, got *Analysis) {
	t.Helper()
	require.NotNil(t, got)
	assert.Equal(t, wantMaxLoadKg, got.MaxLoadKg.String(), "最大桥面载荷")
	assert.Equal(t, wantFirst, got.FirstAxle, "首轴序号")
	assert.Equal(t, wantLast, got.LastAxle, "尾轴序号")
	assert.Equal(t, wantDisplacement, got.DisplacementMm, "发生位移")
	assert.Equal(t, wantConclusion, got.Conclusion, "结论")
}

// windowView 为 Window 的可比较视图（载荷转十进制字符串）。
type windowView struct {
	first, last, displacement int
	load                      string
}

func views(windows []Window) []windowView {
	out := make([]windowView, len(windows))
	for i, w := range windows {
		out[i] = windowView{w.FirstAxle, w.LastAxle, w.DisplacementMm, w.LoadKg.String()}
	}
	return out
}

// intPtr 返回整数值的指针，用于构造选填车速。
func intPtr(v int) *int { return &v }

// assertSegment 校验一个超载区段：起止位移逐项相等，
// 载荷与时长按十进制字符串比较（任意精度）。
func assertSegment(t *testing.T, seg OverloadSegment, wantStart, wantEnd int,
	wantLoadKg, wantDurationMs string) {
	t.Helper()
	assert.Equal(t, wantStart, seg.StartDisplacementMm, "区段起始位移")
	assert.Equal(t, wantEnd, seg.EndDisplacementMm, "区段结束位移")
	assert.Equal(t, wantLoadKg, seg.LoadKg.String(), "区段恒定载荷")
	assert.Equal(t, wantDurationMs, seg.DurationMs.String(), "区段时长毫秒")
}

// 三轴车、桥面有效长度恰好等于首尾轴距：平移至车头轴抵达桥出口、
// 车尾轴抵达桥入口的瞬间（位移 3000），边界上的前后轴均计入载荷，
// 三轴合计 12000 达到峰值。
func TestAnalyze_BoundaryExactFitCountsEdgeAxles(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  12000,
	})
	require.NoError(t, err)
	// 等于核定载荷判定为通行。
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionPass, got)
}

// 同一车辆、核定载荷低于峰值 1 千克：平移后出现的峰值触发拦停。
func TestAnalyze_PeakAfterTranslationStops(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  11999,
	})
	require.NoError(t, err)
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionStop, got)
}

// 并列峰值：{2,3} 轴在位移 2000、{1,2} 轴在位移 4000 都达到 8000，
// 按车辆位移最小确定唯一结果（首轴序号更大的候选反而落选，可锁定裁决次序）。
func TestAnalyze_TiedPeaksChooseEarliestDisplacement(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 2000, 4000},
		AxleLoadsKg:     []int{5000, 3000, 5000},
		BridgeLengthMm:  2000,
		ApprovedLoadKg:  8000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "8000", 2, 3, 2000, ConclusionPass, got)
}

// 相邻轴距大于桥长：两轴不会同时落桥，峰值为较重的单轴。
func TestAnalyze_GapLargerThanBridgeNeverShares(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 6000},
		AxleLoadsKg:     []int{3000, 5000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  10000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "5000", 2, 2, 0, ConclusionPass, got)
}

// 单轴车：进入即峰值，位移为 0。
func TestAnalyze_SingleAxle(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{1200},
		AxleLoadsKg:     []int{9000},
		BridgeLengthMm:  1000,
		ApprovedLoadKg:  9000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "9000", 1, 1, 0, ConclusionPass, got)

	// 同一单轴车，核定载荷差 1 千克即拦停。
	got, err = Analyze(Input{
		AxlePositionsMm: []int{1200},
		AxleLoadsKg:     []int{9000},
		BridgeLengthMm:  1000,
		ApprovedLoadKg:  8999,
	})
	require.NoError(t, err)
	assert.Equal(t, ConclusionStop, got.Conclusion)
}

// 整车长度小于桥长：全部轴同时落桥的时段内载荷为车货总重，
// 最早取得该最大值的位移是车尾轴抵达桥入口的时刻。
func TestAnalyze_WholeVehicleOnBridge(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1000, 2000},
		AxleLoadsKg:     []int{1000, 2000, 3000},
		BridgeLengthMm:  10000,
		ApprovedLoadKg:  6000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "6000", 1, 3, 2000, ConclusionPass, got)
}

// 锁定边界恰好容纳场景的事件扫描序列：同位移处进入先于离开，
// 位移 3000 处先形成三轴全落桥区间，再收缩为两轴区间。
func TestScanWindows_EnterBeforeLeaveAtSameDisplacement(t *testing.T) {
	windows := ScanWindows(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  12000,
	})
	require.Equal(t, []windowView{
		{3, 3, 0, "4000"},
		{2, 3, 1500, "8000"},
		{1, 3, 3000, "12000"},
		{1, 2, 3000, "8000"},
		{1, 1, 4500, "4000"},
	}, views(windows))
}

// 轴距大于桥长时落桥区间会出现空档：空档不产出区间，事件扫描仍完整。
func TestScanWindows_EmptyGapProducesNoWindow(t *testing.T) {
	windows := ScanWindows(Input{
		AxlePositionsMm: []int{0, 6000},
		AxleLoadsKg:     []int{3000, 5000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  10000,
	})
	require.Equal(t, []windowView{
		{2, 2, 0, "5000"},
		{1, 1, 6000, "3000"},
	}, views(windows))
}

// 相同输入两次分析，结果完全一致（唯一确定结论）。
func TestAnalyze_Deterministic(t *testing.T) {
	in := Input{
		AxlePositionsMm: []int{0, 2000, 4000},
		AxleLoadsKg:     []int{5000, 3000, 5000},
		BridgeLengthMm:  2000,
		ApprovedLoadKg:  8000,
	}
	first, err := Analyze(in)
	require.NoError(t, err)
	second, err := Analyze(in)
	require.NoError(t, err)
	assert.Equal(t, first.MaxLoadKg.String(), second.MaxLoadKg.String())
	assert.Equal(t, first, second)
}

// 极大数值不再被拒绝，也不允许溢出：极大轴位置曾使“进入位移+桥长”回绕成
// 负位移、极大载荷曾使求和回绕，导致最大桥面载荷被报成零或少算。
// 现在这类合法输入必须得到精确结果。
func TestAnalyze_ExtremeValuesExact(t *testing.T) {
	t.Run("极大轴位置：离开位移溢出仍精确", func(t *testing.T) {
		// 首尾轴跨度接近 int 上限：后轴的离开位移（进入位移+桥长）超出 int 范围。
		// 两轴不会同时落桥，峰值为车头轴单轴 4000，位移 0。
		got, err := Analyze(Input{
			AxlePositionsMm: []int{0, math.MaxInt64},
			AxleLoadsKg:     []int{4000, 4000},
			BridgeLengthMm:  3000,
			ApprovedLoadKg:  12000,
		})
		require.NoError(t, err)
		assertAnalysis(t, "4000", 2, 2, 0, ConclusionPass, got)
	})

	t.Run("极大载荷：三轴合计超出 int64 仍精确", func(t *testing.T) {
		// 边界恰好容纳场景，三轴各 math.MaxInt64：
		// 峰值 3×9223372036854775807 = 27670116110564327421，不得回绕。
		got, err := Analyze(Input{
			AxlePositionsMm: []int{0, 1500, 3000},
			AxleLoadsKg:     []int{math.MaxInt64, math.MaxInt64, math.MaxInt64},
			BridgeLengthMm:  3000,
			ApprovedLoadKg:  200000,
		})
		require.NoError(t, err)
		assertAnalysis(t, "27670116110564327421", 1, 3, 3000, ConclusionStop, got)
	})

	t.Run("极大位置与载荷混合", func(t *testing.T) {
		// 位置贴近 int 上限但跨度仅 1 毫米：位移很小，载荷合计
		// math.MaxInt64+1 = 9223372036854775808 超出 int64，须精确。
		got, err := Analyze(Input{
			AxlePositionsMm: []int{math.MaxInt64 - 1, math.MaxInt64},
			AxleLoadsKg:     []int{math.MaxInt64, 1},
			BridgeLengthMm:  50000,
			ApprovedLoadKg:  200000,
		})
		require.NoError(t, err)
		assertAnalysis(t, "9223372036854775808", 1, 2, 1, ConclusionStop, got)
	})

	t.Run("双轴极大载荷合计", func(t *testing.T) {
		// 2×math.MaxInt64 = 18446744073709551614（= 2^64-2），int64 装不下。
		got, err := Analyze(Input{
			AxlePositionsMm: []int{math.MaxInt64 - 10, math.MaxInt64},
			AxleLoadsKg:     []int{math.MaxInt64, math.MaxInt64},
			BridgeLengthMm:  50000,
			ApprovedLoadKg:  200000,
		})
		require.NoError(t, err)
		assertAnalysis(t, "18446744073709551614", 1, 2, 10, ConclusionStop, got)
	})
}

// 极大载荷下并列峰值的裁决次序仍然稳定：{2,3} 轴与 {1,2} 轴的合计同为
// math.MaxInt64+1，选择位移更小者。
func TestAnalyze_ExtremeValuesTieBreakStable(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 2000, 4000},
		AxleLoadsKg:     []int{math.MaxInt64, 1, math.MaxInt64},
		BridgeLengthMm:  2000,
		ApprovedLoadKg:  200000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "9223372036854775808", 2, 3, 2000, ConclusionStop, got)
}

// 12 轴重车：全部整数运算在极大载荷下仍精确，落桥 6 轴合计
// 6×math.MaxInt64 = 55340232221128654842。
func TestAnalyze_MaximumVehicleComputesExactly(t *testing.T) {
	positions := make([]int, 0, MaxAxles)
	loads := make([]int, 0, MaxAxles)
	for i := 0; i < MaxAxles; i++ {
		positions = append(positions, i*9000) // 末轴位置 99000
		loads = append(loads, math.MaxInt64)
	}
	got, err := Analyze(Input{
		AxlePositionsMm: positions,
		AxleLoadsKg:     loads,
		BridgeLengthMm:  MaxBridgeLengthMm,
		ApprovedLoadKg:  MaxApprovedLoadKg,
	})
	require.NoError(t, err)
	// 桥长 50000 可容纳连续 6 轴（跨度 45000）。
	assertAnalysis(t, "55340232221128654842", 7, 12, 45000, ConclusionStop, got)
}

func TestValidate_Errors(t *testing.T) {
	valid := func() Input {
		return Input{
			AxlePositionsMm: []int{0, 1500, 3000},
			AxleLoadsKg:     []int{4000, 4000, 4000},
			BridgeLengthMm:  3000,
			ApprovedLoadKg:  12000,
		}
	}
	cases := []struct {
		name      string
		mutate    func(*Input)
		wantInErr string
	}{
		{"轴数为零", func(in *Input) { in.AxlePositionsMm, in.AxleLoadsKg = nil, nil }, "轴数"},
		{"轴数十三", func(in *Input) {
			in.AxlePositionsMm = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
			in.AxleLoadsKg = []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
		}, "轴数"},
		{"位置项数与轴数不一致", func(in *Input) { in.AxlePositionsMm = []int{0, 1500} }, "一致"},
		{"载荷为零", func(in *Input) { in.AxleLoadsKg[1] = 0 }, "载荷"},
		{"载荷为负", func(in *Input) { in.AxleLoadsKg[0] = -100 }, "载荷"},
		{"位置为负", func(in *Input) { in.AxlePositionsMm[0] = -1 }, "不得为负"},
		{"位置重复", func(in *Input) { in.AxlePositionsMm = []int{0, 1500, 1500} }, "重复"},
		{"位置未递增", func(in *Input) { in.AxlePositionsMm = []int{0, 3000, 1500} }, "严格递增"},
		{"桥长低于下限", func(in *Input) { in.BridgeLengthMm = 999 }, "桥面有效长度"},
		{"桥长高于上限", func(in *Input) { in.BridgeLengthMm = 50001 }, "桥面有效长度"},
		{"核定载荷为零", func(in *Input) { in.ApprovedLoadKg = 0 }, "核定载荷"},
		{"核定载荷高于上限", func(in *Input) { in.ApprovedLoadKg = 200001 }, "核定载荷"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.mutate(&in)
			err := Validate(in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantInErr)

			// 非法输入不得产出任何分析结果。
			res, err := Analyze(in)
			require.Error(t, err)
			assert.Nil(t, res)
		})
	}
}

// 边界值合法：桥长 1000/50000、核定载荷 1/200000、位置 0 均须通过校验；
// 轴位置与轴载荷不设上限，极大数值同样合法。
func TestValidate_BoundaryValuesAccepted(t *testing.T) {
	for _, in := range []Input{
		{AxlePositionsMm: []int{0}, AxleLoadsKg: []int{1}, BridgeLengthMm: 1000, ApprovedLoadKg: 1},
		{AxlePositionsMm: []int{0}, AxleLoadsKg: []int{1}, BridgeLengthMm: 50000, ApprovedLoadKg: 200000},
		{AxlePositionsMm: []int{0, 49000}, AxleLoadsKg: []int{1, 1}, BridgeLengthMm: 1000, ApprovedLoadKg: 1},
		{AxlePositionsMm: []int{0, 100000}, AxleLoadsKg: []int{20000, 20000}, BridgeLengthMm: 50000, ApprovedLoadKg: 40000},
		{AxlePositionsMm: []int{0, math.MaxInt64}, AxleLoadsKg: []int{math.MaxInt64, 1}, BridgeLengthMm: 1000, ApprovedLoadKg: 1},
	} {
		assert.NoError(t, Validate(in), "%+v", in)
	}
}

// 载荷合计为任意精度整数：与核定载荷的比较不受 int64 范围限制。
func TestAnalyze_ApprovedComparisonExact(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500},
		AxleLoadsKg:     []int{math.MaxInt64, 1},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  200000,
	})
	require.NoError(t, err)
	assert.Equal(t, "9223372036854775808", got.MaxLoadKg.String())
	assert.Equal(t, ConclusionStop, got.Conclusion)

	// 核定载荷恰为上限 200000 时，单轴 200000 仍判定通行（等于不拦停）。
	got, err = Analyze(Input{
		AxlePositionsMm: []int{0},
		AxleLoadsKg:     []int{200000},
		BridgeLengthMm:  1000,
		ApprovedLoadKg:  200000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "200000", 1, 1, 0, ConclusionPass, got)
}

// 防止 big.Int 在窗口间共享底层数组：先扫描出的区间载荷不得被后续事件改写。
func TestScanWindows_LoadSnapshotsIndependent(t *testing.T) {
	windows := ScanWindows(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{math.MaxInt64, 2, 3},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  12000,
	})
	require.Len(t, windows, 5)
	// 第一个窗口（位移 0，仅车头轴，载荷 3）若被后续加减改写则不再是 3。
	assert.Equal(t, "3", windows[0].LoadKg.String())
	assert.Equal(t, "5", windows[1].LoadKg.String())
	assert.Equal(t, "9223372036854775812", windows[2].LoadKg.String()) // MaxInt64+2+3
}

// 持续超载：三轴车以 7 毫米每秒通过，位移 1500-4500 区间载荷恒定 8000 千克
// 超过核定值 7000。位移 3000 处先进入后离开的瞬时峰值 12000 仍参与峰值裁决，
// 但其位移长度为零不进入区段；两侧 8000 千克区段相邻且载荷相同，合并为一段。
// 时长 3000×1000÷7 = 428571.43 向上取整为 428572 毫秒。
func TestAnalyze_OverloadSegmentsContinuousOverload(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  7000,
		SpeedMmPerS:     intPtr(7),
	})
	require.NoError(t, err)
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionStop, got)
	require.Len(t, got.OverloadSegments, 1)
	assertSegment(t, got.OverloadSegments[0], 1500, 4500, "8000", "428572")
	require.NotNil(t, got.OverloadDurationMs)
	assert.Equal(t, "428572", got.OverloadDurationMs.String(), "累计超载时长")
}

// 载荷变化：相邻超载区段载荷分别为 7000 与 9000 千克，载荷不同不得合并。
// 车速 1000 毫米每秒时每段 1500 毫米恰好 1500 毫秒（整除不向上进位）。
func TestAnalyze_OverloadSegmentsDifferentLoadsNotMerged(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{5000, 4000, 3000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  6000,
		SpeedMmPerS:     intPtr(1000),
	})
	require.NoError(t, err)
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionStop, got)
	require.Len(t, got.OverloadSegments, 2)
	assertSegment(t, got.OverloadSegments[0], 1500, 3000, "7000", "1500")
	assertSegment(t, got.OverloadSegments[1], 3000, 4500, "9000", "1500")
	assert.Equal(t, "3000", got.OverloadDurationMs.String(), "累计超载时长")
}

// 同位移进出：车尾轴恰抵桥出口的瞬间车头轴恰抵桥入口，瞬时两轴同桥
// 10000 千克超过核定值 8000 触发拦停；但瞬时状态位移长度为零，
// 不进入区段与累计时长——区段清单为空、累计时长为零。
func TestAnalyze_InstantaneousOverloadOnlyAffectsPeak(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 3000},
		AxleLoadsKg:     []int{5000, 5000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  8000,
		SpeedMmPerS:     intPtr(1000),
	})
	require.NoError(t, err)
	assertAnalysis(t, "10000", 1, 2, 3000, ConclusionStop, got)
	require.NotNil(t, got.OverloadSegments, "提供车速时区段清单不得为 nil")
	assert.Empty(t, got.OverloadSegments)
	require.NotNil(t, got.OverloadDurationMs)
	assert.Equal(t, "0", got.OverloadDurationMs.String(), "累计超载时长")
}

// 同位移进出且两侧区段载荷相同：瞬时峰值 10000 只参与峰值裁决，
// 两侧 5000 千克区段相邻同载荷合并为 [0,6000] 一段。车速 7 毫米每秒时
// 合并后时长 6000×1000÷7 向上取整为 857143 毫秒；若先各自取整再相加
// 会得到 2×428572 = 857144——锁定“先合并再按时长公式计算”的次序。
func TestAnalyze_SameLoadSegmentsMergedBeforeDuration(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 3000},
		AxleLoadsKg:     []int{5000, 5000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  4000,
		SpeedMmPerS:     intPtr(7),
	})
	require.NoError(t, err)
	assertAnalysis(t, "10000", 1, 2, 3000, ConclusionStop, got)
	require.Len(t, got.OverloadSegments, 1)
	assertSegment(t, got.OverloadSegments[0], 0, 6000, "5000", "857143")
	assert.Equal(t, "857143", got.OverloadDurationMs.String(), "累计超载时长")
}

// 未提供车速：不计算超载区段与累计时长，两个新字段均为 nil，
// 分析行为与引入车速能力前完全一致。
func TestAnalyze_NoSpeedLeavesOverloadFieldsNil(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  12000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionPass, got)
	assert.Nil(t, got.OverloadSegments)
	assert.Nil(t, got.OverloadDurationMs)
}

// 极大轴位置下提供车速：车头轴在桥 3000 毫米超载正常产出区段；
// 车尾轴的离开位移超出 int 范围，其超载区间不产出区段
// （真实结束位移无法用 int 表示，与溢出事件不产出窗口快照同理）。
func TestAnalyze_ExtremePositionsWithSpeed(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, math.MaxInt64},
		AxleLoadsKg:     []int{4000, 5000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  3000,
		SpeedMmPerS:     intPtr(1000),
	})
	require.NoError(t, err)
	assertAnalysis(t, "5000", 2, 2, 0, ConclusionStop, got)
	require.Len(t, got.OverloadSegments, 1)
	assertSegment(t, got.OverloadSegments[0], 0, 3000, "5000", "3000")
	assert.Equal(t, "3000", got.OverloadDurationMs.String())
}

// 车速校验：零、负值与越界均非法；边界 1 与 50000 毫米每秒合法。
func TestValidate_SpeedRange(t *testing.T) {
	valid := func() Input {
		return Input{
			AxlePositionsMm: []int{0, 1500, 3000},
			AxleLoadsKg:     []int{4000, 4000, 4000},
			BridgeLengthMm:  3000,
			ApprovedLoadKg:  12000,
		}
	}
	for _, speed := range []int{0, -1, -100, 50001, 100000} {
		in := valid()
		in.SpeedMmPerS = intPtr(speed)
		err := Validate(in)
		require.Error(t, err, "车速 %d 应非法", speed)
		assert.Contains(t, err.Error(), "车速")

		res, err := Analyze(in)
		require.Error(t, err)
		assert.Nil(t, res, "非法车速不得产出任何分析结果")
	}
	for _, speed := range []int{MinSpeedMmPerS, MaxSpeedMmPerS} {
		in := valid()
		in.SpeedMmPerS = intPtr(speed)
		assert.NoError(t, Validate(in), "车速 %d 应合法", speed)
	}
}
