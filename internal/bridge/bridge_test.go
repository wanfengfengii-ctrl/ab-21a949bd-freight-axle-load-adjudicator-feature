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

// ---- 选填预计车速：超载区段与累计时长 ----

// segmentView 为 OverloadSegment 的可比较视图（全部转十进制字符串）。
type segmentView struct {
	start, end, load, durationMs string
}

func segmentViews(segments []OverloadSegment) []segmentView {
	out := make([]segmentView, len(segments))
	for i, s := range segments {
		out[i] = segmentView{
			s.StartDisplacementMm.String(),
			s.EndDisplacementMm.String(),
			s.LoadKg.String(),
			s.DurationMs.String(),
		}
	}
	return out
}

func speedPtr(v int) *int { return &v }

// 持续超载：两轴先后进入形成一段长度 2000 毫米、恒定载荷 14000 的超载区段，
// 两侧 7000 均不超限；毫秒数按“位移差×1000÷车速”向上取整锁定。
func TestAnalyze_ContinuousOverloadSegmentRoundsUp(t *testing.T) {
	// 位置 [0,1000]、桥长 3000：事件序列为
	// 0 车头轴进入 L=7000；1000 车尾轴进入 L=14000；3000 车头轴离开 L=7000；
	// 4000 车尾轴离开。唯一超载区间 [1000,3000) 载荷 14000，长度 2000。
	in := Input{
		AxlePositionsMm: []int{0, 1000},
		AxleLoadsKg:     []int{7000, 7000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  10000,
		SpeedMmPerS:     speedPtr(3000), // 2000mm × 1000 ÷ 3000 = 666.66… → 667
	}
	got, err := Analyze(in)
	require.NoError(t, err)
	// 原峰值裁决不变：峰值 14000，发生在位移 1000 的两轴区间，拦停。
	assertAnalysis(t, "14000", 1, 2, 1000, ConclusionStop, got)
	require.Equal(t, []segmentView{
		{"1000", "3000", "14000", "667"},
	}, segmentViews(got.OverloadSegments))
	assert.Equal(t, "667", got.TotalOverloadDurationMs.String())

	// 同一区段、车速 1000 毫米每秒时恰好 2000 毫秒（整除不额外进位）。
	*in.SpeedMmPerS = 1000
	got, err = Analyze(in)
	require.NoError(t, err)
	require.Len(t, got.OverloadSegments, 1)
	assert.Equal(t, "2000", got.OverloadSegments[0].DurationMs.String())
	assert.Equal(t, "2000", got.TotalOverloadDurationMs.String())

	// 车速上限 50000、位移差 1 毫米时仍向上取整为 1 毫秒（不得取整为 0）。
	// 位置 [0,1000]、桥长 1001：进入 0/1000、离开 1001/2001，
	// 唯一两轴超载区间 [1000,1001)，长度 1。
	one := Input{
		AxlePositionsMm: []int{0, 1000},
		AxleLoadsKg:     []int{7000, 7000},
		BridgeLengthMm:  1001,
		ApprovedLoadKg:  10000,
		SpeedMmPerS:     speedPtr(MaxSpeedMmPerS),
	}
	got, err = Analyze(one)
	require.NoError(t, err)
	require.Equal(t, []segmentView{{"1000", "1001", "14000", "1"}},
		segmentViews(got.OverloadSegments))
	assert.Equal(t, "1", got.TotalOverloadDurationMs.String())
}

// 载荷变化：平移过程中恒定载荷随进入事件逐段变化，各相邻区段载荷不同，
// 必须逐段返回而不得合并；累计时长为五段之和。
func TestAnalyze_DifferentConstantLoadsAreNotMerged(t *testing.T) {
	// 位置 [0,1000,2000]、载荷 [6000,1000,6000]、桥长 3000、核定 5000：
	// [0,1000)=6000、[1000,2000)=7000、[2000,3000)=13000、
	// [3000,4000)=7000、[4000,5000)=6000，全部超载且相邻载荷互不相等。
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1000, 2000},
		AxleLoadsKg:     []int{6000, 1000, 6000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  5000,
		SpeedMmPerS:     speedPtr(1000),
	})
	require.NoError(t, err)
	assertAnalysis(t, "13000", 1, 3, 2000, ConclusionStop, got)
	require.Equal(t, []segmentView{
		{"0", "1000", "6000", "1000"},
		{"1000", "2000", "7000", "1000"},
		{"2000", "3000", "13000", "1000"},
		{"3000", "4000", "7000", "1000"},
		{"4000", "5000", "6000", "1000"},
	}, segmentViews(got.OverloadSegments))
	assert.Equal(t, "5000", got.TotalOverloadDurationMs.String())
}

// 同位移处进入先于离开产生的瞬时峰值只参与原峰值裁决：核定载荷介于两侧
// 恒定载荷与瞬时峰值之间时，峰值仍触发拦停，但没有任何位移长度大于零的
// 超载区段，区段列表为空、累计时长为零。
func TestAnalyze_InstantPeakAtSameDisplacementNoSegment(t *testing.T) {
	// 边界恰好容纳场景：位移 3000 处先进入车尾轴形成三轴 12000 的瞬时窗口，
	// 随即离开车头轴恢复两轴 8000；其余正长度区间载荷为 4000 或 8000。
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  11000, // 8000 合规、12000 瞬时超载
		SpeedMmPerS:     speedPtr(1000),
	})
	require.NoError(t, err)
	// 原峰值裁决：瞬时 12000 仍是峰值，拦停结论不变。
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionStop, got)
	assert.Empty(t, got.OverloadSegments, "瞬时峰值不得进入区段")
	assert.Equal(t, "0", got.TotalOverloadDurationMs.String(), "瞬时峰值累计时长为零")
}

// 同位移瞬时窗口两侧为相同的超载恒定载荷时，瞬时窗口不隔开、也不进入区段，
// 两侧先合并为一个区段，毫秒数按合并后的总位移差一次向上取整。
func TestAnalyze_AdjacentSameLoadMergedAcrossInstantPeak(t *testing.T) {
	// 位置 [0,3000]、两轴各 5000、桥长 3000、核定 4000：
	// [0,3000) 仅车头轴 5000；位移 3000 瞬时两轴 10000；[3000,6000) 仅车尾轴 5000。
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 3000},
		AxleLoadsKg:     []int{5000, 5000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  4000,
		SpeedMmPerS:     speedPtr(700), // 6000×1000÷700 = 8571.428… → 8572
	})
	require.NoError(t, err)
	assertAnalysis(t, "10000", 1, 2, 3000, ConclusionStop, got)
	require.Equal(t, []segmentView{
		{"0", "6000", "5000", "8572"},
	}, segmentViews(got.OverloadSegments))
	assert.Equal(t, "8572", got.TotalOverloadDurationMs.String())
}

// 峰值未超过核定载荷（通行）时不返回任何超载区段，累计时长为零。
func TestAnalyze_NoOverloadWhenPassing(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1000},
		AxleLoadsKg:     []int{7000, 7000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  14000, // 峰值恰好等于核定值，通行
		SpeedMmPerS:     speedPtr(1000),
	})
	require.NoError(t, err)
	assert.Equal(t, ConclusionPass, got.Conclusion)
	assert.Empty(t, got.OverloadSegments)
	assert.Equal(t, "0", got.TotalOverloadDurationMs.String())
}

// 未提供车速时只执行原峰值分析：不产出区段与累计时长，Analysis 与引入车速
// 能力前一致（HTTP 层的显式 null 同样映射为缺省）。
func TestAnalyze_WithoutSpeedKeepsOriginalResult(t *testing.T) {
	got, err := Analyze(Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  11000,
	})
	require.NoError(t, err)
	assertAnalysis(t, "12000", 1, 3, 3000, ConclusionStop, got)
	assert.Nil(t, got.OverloadSegments)
	assert.Nil(t, got.TotalOverloadDurationMs)
}

// 预计车速校验：类型外的零、负值与超上限值非法，1 与 50000 合法。
func TestValidate_SpeedRange(t *testing.T) {
	valid := Input{
		AxlePositionsMm: []int{0, 1500, 3000},
		AxleLoadsKg:     []int{4000, 4000, 4000},
		BridgeLengthMm:  3000,
		ApprovedLoadKg:  12000,
	}
	for _, v := range []int{0, -1, MinSpeedMmPerS - 1, MaxSpeedMmPerS + 1} {
		in := valid
		in.SpeedMmPerS = speedPtr(v)
		err := Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "车速")
		res, analyzeErr := Analyze(in)
		require.Error(t, analyzeErr)
		assert.Nil(t, res)
	}
	for _, v := range []int{MinSpeedMmPerS, MaxSpeedMmPerS} {
		in := valid
		in.SpeedMmPerS = speedPtr(v)
		assert.NoError(t, Validate(in))
	}
}

// 极大数值场景：极大轴位置下区段起止位移与毫秒数仍按任意精度整数精确给出。
func TestAnalyze_OverloadSegmentsExtremeDisplacementExact(t *testing.T) {
	t.Run("极大位置附近多段精确", func(t *testing.T) {
		// 两轴位置 MaxInt64-1000 与 MaxInt64，桥长 50000：车头轴位移 0 进入、
		// 50000 离开；车尾轴位移 1000 进入、51000 离开。
		// [0,1000) 与 [50000,51000) 仅单轴 math.MaxInt64，[1000,50000) 两轴合计超载。
		got, err := Analyze(Input{
			AxlePositionsMm: []int{math.MaxInt64 - 1000, math.MaxInt64},
			AxleLoadsKg:     []int{math.MaxInt64, math.MaxInt64},
			BridgeLengthMm:  50000,
			ApprovedLoadKg:  200000,
			SpeedMmPerS:     speedPtr(1),
		})
		require.NoError(t, err)
		require.Len(t, got.OverloadSegments, 3)
		assert.Equal(t, segmentView{
			"0", "1000", "9223372036854775807", "1000000",
		}, segmentViews(got.OverloadSegments)[0])
		assert.Equal(t, segmentView{
			"1000", "50000", "18446744073709551614", "49000000",
		}, segmentViews(got.OverloadSegments)[1])
		assert.Equal(t, segmentView{
			"50000", "51000", "9223372036854775807", "1000000",
		}, segmentViews(got.OverloadSegments)[2])
		assert.Equal(t, "51000000", got.TotalOverloadDurationMs.String())
	})

	t.Run("离开位移超出 int64 仍精确", func(t *testing.T) {
		// 两轴位置 0 与 MaxInt64（不会同时落桥）：车尾轴进入位移 MaxInt64、
		// 离开位移 MaxInt64+1000 超出 int64；其单轴超载区段终点与毫秒数
		// （1000 毫米 ÷ 1 毫米每秒 = 1000000 毫秒）仍须精确，不回绕。
		got, err := Analyze(Input{
			AxlePositionsMm: []int{0, math.MaxInt64},
			AxleLoadsKg:     []int{math.MaxInt64, math.MaxInt64},
			BridgeLengthMm:  1000,
			ApprovedLoadKg:  200000,
			SpeedMmPerS:     speedPtr(1),
		})
		require.NoError(t, err)
		require.Len(t, got.OverloadSegments, 2)
		assert.Equal(t, segmentView{
			"0", "1000", "9223372036854775807", "1000000",
		}, segmentViews(got.OverloadSegments)[0])
		assert.Equal(t, segmentView{
			"9223372036854775807", "9223372036854776807", "9223372036854775807", "1000000",
		}, segmentViews(got.OverloadSegments)[1])
		assert.Equal(t, "2000000", got.TotalOverloadDurationMs.String())
	})
}
