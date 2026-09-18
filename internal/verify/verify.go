// Package verify 实现货车轴组划分与超限裁决规则，不包含任何 HTTP 细节。
package verify

import (
	"fmt"
	"sort"
)

// 输入与裁决常量，单位见字段名。
const (
	MinAxles       = 1
	MaxAxles       = 12
	MinAxleLoadKg  = 1
	MaxAxleLoadKg  = 20000
	MinSpacingMm   = 500
	MaxSpacingMm   = 10000
	SameGroupMaxMm = 1800 // 相邻轴距 <= 1800mm 同组，> 1800mm 另起一组
	VehicleLimitKg = 49000

	MinScaleWeightKg = 1
	MaxScaleWeightKg = 240000
	// MaxScaleDeviationPercent 为地磅重量与轴载荷合计允许的最大偏差百分比（含边界）。
	MaxScaleDeviationPercent = 5
)

// groupLimitKg 按轴组内轴数给出限值：单轴 10000、双轴 18000、三轴 24000。
var groupLimitKg = map[int]int{
	1: 10000,
	2: 18000,
	3: 24000,
}

// GroupResult 为单个轴组的裁决结果，轴序号均按车头到车尾从 1 开始编号。
type GroupResult struct {
	Index     int  `json:"index"`      // 组序号，从 1 开始
	StartAxle int  `json:"start_axle"` // 组内首轴序号
	EndAxle   int  `json:"end_axle"`   // 组内尾轴序号
	AxleCount int  `json:"axle_count"` // 组内轴数（1-3）
	LoadKg    int  `json:"load_kg"`    // 组载荷，整数求和
	LimitKg   int  `json:"limit_kg"`   // 适用限值
	OverLimit bool `json:"over_limit"` // 是否超限；等于限值判定为合规
}

// VehicleResult 为整车合计裁决结果。
type VehicleResult struct {
	LoadKg    int  `json:"load_kg"`
	LimitKg   int  `json:"limit_kg"`
	OverLimit bool `json:"over_limit"`
}

// Violation 标记一处超限：轴组超限按组序号升序排列，整车超限固定在最后。
type Violation struct {
	Scope string `json:"scope"`           // "group" 或 "vehicle"
	Index int    `json:"index,omitempty"` // scope 为 group 时的组序号
}

// CalibrationResult 记录一次地磅校准的输入与结果。
// 校准后各轴之和严格等于地磅重量，轴组与整车裁决均使用校准值。
type CalibrationResult struct {
	ScaleWeightKg     int   `json:"scale_weight_kg"`     // 收费站地磅整车重量
	OriginalTotalKg   int   `json:"original_total_kg"`   // 校准前轴载荷合计
	DifferenceKg      int   `json:"difference_kg"`       // 校准差额 = 地磅重量 − 原始合计（可正可负）
	CalibratedLoadsKg []int `json:"calibrated_loads_kg"` // 各轴校准载荷，轴序与请求一致
}

// Result 是一次复核的完整裁决结果，不含任何错误信息（非法输入不会产生部分结果）。
type Result struct {
	Groups      []GroupResult      `json:"groups"`
	Vehicle     VehicleResult      `json:"vehicle"`
	Violations  []Violation        `json:"violations"`
	Calibration *CalibrationResult `json:"calibration,omitempty"` // 仅携带地磅重量的请求存在
}

// 比对结论：两份裁决均合法且执法结论一致时为“结论确认”，
// 任一分组边界、轴覆盖区间超限状态或整车结论发生变化时为“结论改变”。
const (
	ConclusionConfirmed = "结论确认"
	ConclusionChanged   = "结论改变"
)

// RetestMeasurement 为一次称重测量的输入，字段契约与单次裁决完全一致：
// 轴载荷、相邻轴距必填，ScaleWeightKg 为可选地磅整车重量（nil 表示不校准）。
type RetestMeasurement struct {
	AxleLoadsKg    []int
	AxleSpacingsMm []int
	ScaleWeightKg  *int
}

// AxleRange 为轴覆盖闭区间（轴序号 1 基，首尾轴均含）。
type AxleRange struct {
	StartAxle int `json:"start_axle"` // 区间首轴序号
	EndAxle   int `json:"end_axle"`   // 区间尾轴序号
}

// GroupBoundaryChange 标识首次与重测之间分组边界发生变化的轴覆盖区间，
// 并分别给出两份裁决在该区间内的分组覆盖（已按区间裁剪，按首轴序号排序）。
type GroupBoundaryChange struct {
	StartAxle    int         `json:"start_axle"`
	EndAxle      int         `json:"end_axle"`
	FirstGroups  []AxleRange `json:"first_groups"`  // 首次裁决在该区间内的分组覆盖
	RetestGroups []AxleRange `json:"retest_groups"` // 重测裁决在该区间内的分组覆盖
}

// OverLimitChange 标识一个轴覆盖区间上“轴是否属于超限轴组”的状态发生翻转，
// 区间内每个轴在同一份裁决中的状态一致；分组重排时区间按轴逐轴比对后合并得出。
type OverLimitChange struct {
	StartAxle       int  `json:"start_axle"`
	EndAxle         int  `json:"end_axle"`
	FirstOverLimit  bool `json:"first_over_limit"`  // 区间在首次裁决中是否属于超限轴组
	RetestOverLimit bool `json:"retest_over_limit"` // 区间在重测裁决中是否属于超限轴组
}

// VehicleConclusionChange 仅在整车超限结论翻转时出现，并携带两侧整车总重备查。
type VehicleConclusionChange struct {
	FirstLoadKg     int  `json:"first_load_kg"`
	RetestLoadKg    int  `json:"retest_load_kg"`
	FirstOverLimit  bool `json:"first_over_limit"`
	RetestOverLimit bool `json:"retest_over_limit"`
}

// ComparisonResult 是同一车辆首次称重与重测的比对结果：
// 两份完整裁决各自独立给出，随后只列变化项；无变化时各变化列表为空数组。
type ComparisonResult struct {
	FirstResult             *Result                  `json:"first_result"`
	RetestResult            *Result                  `json:"retest_result"`
	GroupBoundaryChanges    []GroupBoundaryChange    `json:"group_boundary_changes"`
	OverLimitChanges        []OverLimitChange        `json:"over_limit_changes"`
	VehicleConclusionChange *VehicleConclusionChange `json:"vehicle_conclusion_change,omitempty"`
	Conclusion              string                   `json:"conclusion"`
}

// Validate 仅校验输入合法性，不产出裁决结果。
func Validate(axleLoadsKg []int, axleSpacingsMm []int) error {
	n := len(axleLoadsKg)
	if n < MinAxles || n > MaxAxles {
		return fmt.Errorf("轴数须为 %d 至 %d，实际为 %d", MinAxles, MaxAxles, n)
	}
	if len(axleSpacingsMm) != n-1 {
		return fmt.Errorf("轴距项数必须比轴数少一项：轴数 %d 时应为 %d，实际为 %d",
			n, n-1, len(axleSpacingsMm))
	}
	for i, load := range axleLoadsKg {
		if load < MinAxleLoadKg || load > MaxAxleLoadKg {
			return fmt.Errorf("第 %d 轴载荷 %d 超出允许范围 %d-%d 千克",
				i+1, load, MinAxleLoadKg, MaxAxleLoadKg)
		}
	}
	for i, spacing := range axleSpacingsMm {
		if spacing < MinSpacingMm || spacing > MaxSpacingMm {
			return fmt.Errorf("第 %d 项轴距 %d 超出允许范围 %d-%d 毫米",
				i+1, spacing, MinSpacingMm, MaxSpacingMm)
		}
	}
	return nil
}

// axleSpan 描述一个轴组覆盖的轴下标区间（闭区间，0 基）。
type axleSpan struct {
	start int
	end   int
}

// splitGroups 按 1800mm 临界规则划分轴组：间距 <= 1800 同组，> 1800 开启下一组。
func splitGroups(axleCount int, spacingsMm []int) []axleSpan {
	spans := make([]axleSpan, 0, axleCount)
	start := 0
	for i := 0; i < axleCount-1; i++ {
		if spacingsMm[i] > SameGroupMaxMm {
			spans = append(spans, axleSpan{start: start, end: i})
			start = i + 1
		}
	}
	spans = append(spans, axleSpan{start: start, end: axleCount - 1})
	return spans
}

// Evaluate 校验并裁决。任何输入非法都返回 error，调用方必须整体拒绝，不得使用部分结果。
func Evaluate(axleLoadsKg []int, axleSpacingsMm []int) (*Result, error) {
	if err := Validate(axleLoadsKg, axleSpacingsMm); err != nil {
		return nil, err
	}
	return evaluate(axleLoadsKg, axleSpacingsMm)
}

// EvaluateWithScale 先按地磅整车重量校准各轴载荷，再按现有轴距分组与限值裁决。
// 地磅重量超出 1-240000 千克，或与轴载荷合计的偏差超过 5% 时返回 error，
// 调用方必须整体拒绝，不得使用部分结果。
func EvaluateWithScale(axleLoadsKg []int, axleSpacingsMm []int, scaleWeightKg int) (*Result, error) {
	if err := Validate(axleLoadsKg, axleSpacingsMm); err != nil {
		return nil, err
	}
	if scaleWeightKg < MinScaleWeightKg || scaleWeightKg > MaxScaleWeightKg {
		return nil, fmt.Errorf("地磅整车重量 %d 超出允许范围 %d-%d 千克",
			scaleWeightKg, MinScaleWeightKg, MaxScaleWeightKg)
	}
	total := 0
	for _, load := range axleLoadsKg {
		total += load
	}
	diff := scaleWeightKg - total
	// 偏差超过 5% 才拒绝（恰好 5% 允许）：|diff|/total > 5% 等价于
	// |diff|*100 > total*5，全程整数比较，避免浮点误差影响边界判定。
	if absInt(diff)*100 > total*MaxScaleDeviationPercent {
		return nil, fmt.Errorf("地磅整车重量与轴载荷合计的偏差超过 %d%%，不予校准", MaxScaleDeviationPercent)
	}

	calibrated := Calibrate(axleLoadsKg, scaleWeightKg)
	res, err := evaluate(calibrated, axleSpacingsMm)
	if err != nil {
		return nil, err
	}
	res.Calibration = &CalibrationResult{
		ScaleWeightKg:     scaleWeightKg,
		OriginalTotalKg:   total,
		DifferenceKg:      diff,
		CalibratedLoadsKg: calibrated,
	}
	return res, nil
}

// Calibrate 把地磅重量与轴载荷合计的差额按各轴原载荷比例分摊：
// 每轴先取精确份额的向下取整部分，剩余整数千克按小数部分从大到小、
// 轴序号从小到大逐轴补 1，保证校准后各轴之和严格等于地磅重量，
// 且同一输入永远得到同一结果。调用前必须完成 Validate 与偏差校验。
func Calibrate(axleLoadsKg []int, scaleWeightKg int) []int {
	total := 0
	for _, load := range axleLoadsKg {
		total += load
	}
	diff := scaleWeightKg - total

	n := len(axleLoadsKg)
	floors := make([]int, n)
	remainders := make([]int, n) // 各轴份额的小数部分分子（分母同为 total），取值 [0, total)
	rest := diff                 // 向下取整后尚需补齐的整数千克，等于余数分子之和 / total
	for i, load := range axleLoadsKg {
		floors[i] = floorDiv(diff*load, total)
		remainders[i] = diff*load - floors[i]*total
		rest -= floors[i]
	}

	// 补齐顺序：小数部分大的优先，相等时轴序号小的优先。
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		i, j := order[a], order[b]
		if remainders[i] != remainders[j] {
			return remainders[i] > remainders[j]
		}
		return i < j
	})

	calibrated := make([]int, n)
	for i, load := range axleLoadsKg {
		calibrated[i] = load + floors[i]
	}
	for k := 0; k < rest; k++ {
		calibrated[order[k]]++
	}
	return calibrated
}

// floorDiv 返回 a/b 向下取整（向负无穷方向）的商，b 必须为正数。
func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// evaluateMeasurement 按单次裁决契约执行一次测量：带地磅重量时先校准再裁决，
// 否则直接裁决；任一步非法都返回 error，绝不产出部分结果。
func evaluateMeasurement(m RetestMeasurement) (*Result, error) {
	if m.ScaleWeightKg != nil {
		return EvaluateWithScale(m.AxleLoadsKg, m.AxleSpacingsMm, *m.ScaleWeightKg)
	}
	return Evaluate(m.AxleLoadsKg, m.AxleSpacingsMm)
}

// CompareRetest 接收同一车辆的首次称重与重测数据，分别走既有裁决链路，
// 再比对分组边界、各轴覆盖区间的超限状态与整车结论。
// 任一份数据非法、或两份轴数不一致时返回 error；调用方必须整体拒绝，
// 不得在错误中夹带另一份裁决结果。
func CompareRetest(first, retest RetestMeasurement) (*ComparisonResult, error) {
	// 先各做一次纯输入校验，轴数比对只看载荷项数：轴数须相同。
	if err := Validate(first.AxleLoadsKg, first.AxleSpacingsMm); err != nil {
		return nil, fmt.Errorf("首次称重数据非法：%s", err.Error())
	}
	if err := Validate(retest.AxleLoadsKg, retest.AxleSpacingsMm); err != nil {
		return nil, fmt.Errorf("重测数据非法：%s", err.Error())
	}
	if len(first.AxleLoadsKg) != len(retest.AxleLoadsKg) {
		return nil, fmt.Errorf("首次与重测轴数不一致：首次为 %d 轴，重测为 %d 轴",
			len(first.AxleLoadsKg), len(retest.AxleLoadsKg))
	}

	firstResult, err := evaluateMeasurement(first)
	if err != nil {
		// 校验已先行通过，这里只可能是四轴组等裁决期非法情形。
		return nil, fmt.Errorf("首次称重数据非法：%s", err.Error())
	}
	retestResult, err := evaluateMeasurement(retest)
	if err != nil {
		return nil, fmt.Errorf("重测数据非法：%s", err.Error())
	}

	axleCount := len(first.AxleLoadsKg)
	boundaryChanges := diffGroupBoundaries(axleCount, first.AxleSpacingsMm, retest.AxleSpacingsMm,
		firstResult.Groups, retestResult.Groups)
	overLimitChanges := diffOverLimitByAxle(axleCount, firstResult.Groups, retestResult.Groups)

	var vehicleChange *VehicleConclusionChange
	if firstResult.Vehicle.OverLimit != retestResult.Vehicle.OverLimit {
		vehicleChange = &VehicleConclusionChange{
			FirstLoadKg:     firstResult.Vehicle.LoadKg,
			RetestLoadKg:    retestResult.Vehicle.LoadKg,
			FirstOverLimit:  firstResult.Vehicle.OverLimit,
			RetestOverLimit: retestResult.Vehicle.OverLimit,
		}
	}

	conclusion := ConclusionConfirmed
	if len(boundaryChanges) > 0 || len(overLimitChanges) > 0 || vehicleChange != nil {
		conclusion = ConclusionChanged
	}
	return &ComparisonResult{
		FirstResult:             firstResult,
		RetestResult:            retestResult,
		GroupBoundaryChanges:    boundaryChanges,
		OverLimitChanges:        overLimitChanges,
		VehicleConclusionChange: vehicleChange,
		Conclusion:              conclusion,
	}, nil
}

// groupSpans 把裁决结果里的组转成 0 基闭区间列表，结果本身已按首轴排序。
func groupSpans(groups []GroupResult) []axleSpan {
	spans := make([]axleSpan, len(groups))
	for i, g := range groups {
		spans[i] = axleSpan{start: g.StartAxle - 1, end: g.EndAxle - 1}
	}
	return spans
}

// overlaps 判断两个闭区间是否相交。
func overlaps(a, b axleSpan) bool {
	return a.start <= b.end && b.start <= a.end
}

// clipTo 把区间 s 裁剪到 window 内，调用前须保证两者相交。
func clipTo(s, window axleSpan) axleSpan {
	return axleSpan{start: maxInt(s.start, window.start), end: minInt(s.end, window.end)}
}

// toRanges 将若干 0 基区间转成 1 基的 JSON 轴覆盖区间，均已按首轴排序。
func toRanges(spans []axleSpan) []AxleRange {
	ranges := make([]AxleRange, len(spans))
	for i, s := range spans {
		ranges[i] = AxleRange{StartAxle: s.start + 1, EndAxle: s.end + 1}
	}
	return ranges
}

// diffGroupBoundaries 按轴覆盖区间识别分组边界变化：
// 逐项比较两侧相邻轴距是否为组间边界（>1800mm），把连续的“边界状态翻转”
// 的相邻轴距所覆盖的车轴合并为一个变化段（等价于以翻转边界为边的最大连通轴段）；
// 两侧同为边界或同为非边界的轴距不产生变化段。段内再分别给出两侧分组覆盖
// （按区间裁剪）。结果按首轴序号（车头方向）稳定升序。
func diffGroupBoundaries(axleCount int, firstSpacings, retestSpacings []int,
	firstGroups, retestGroups []GroupResult) []GroupBoundaryChange {
	boundary := func(spacings []int, sp int) bool { return spacings[sp] > SameGroupMaxMm }
	boundaryChanged := func(sp int) bool {
		return boundary(firstSpacings, sp) != boundary(retestSpacings, sp)
	}

	type segment struct{ start, end int }
	segments := make([]segment, 0)
	for sp := 0; sp < axleCount-1; {
		if !boundaryChanged(sp) {
			sp++
			continue
		}
		start := sp
		for sp < axleCount-1 && boundaryChanged(sp) {
			sp++
		}
		// 翻转的间距从 start 连到 sp-1，覆盖车轴 start..sp。
		segments = append(segments, segment{start: start, end: sp})
	}

	first := groupSpans(firstGroups)
	retest := groupSpans(retestGroups)
	changes := make([]GroupBoundaryChange, 0, len(segments))
	for _, seg := range segments {
		window := axleSpan{start: seg.start, end: seg.end}
		var fParts, rParts []axleSpan
		for _, s := range first {
			if overlaps(s, window) {
				fParts = append(fParts, clipTo(s, window))
			}
		}
		for _, s := range retest {
			if overlaps(s, window) {
				rParts = append(rParts, clipTo(s, window))
			}
		}
		changes = append(changes, GroupBoundaryChange{
			StartAxle:    seg.start + 1,
			EndAxle:      seg.end + 1,
			FirstGroups:  toRanges(fParts),
			RetestGroups: toRanges(rParts),
		})
	}
	return changes
}

// diffOverLimitByAxle 把两侧“每个轴所属轴组是否超限”逐轴比对，
// 再将状态相同的连续变化轴合并为轴覆盖区间，按首轴序号升序输出。
func diffOverLimitByAxle(axleCount int, firstGroups, retestGroups []GroupResult) []OverLimitChange {
	firstOver := make([]bool, axleCount)
	retestOver := make([]bool, axleCount)
	for _, g := range firstGroups {
		if g.OverLimit {
			for axle := g.StartAxle - 1; axle <= g.EndAxle-1; axle++ {
				firstOver[axle] = true
			}
		}
	}
	for _, g := range retestGroups {
		if g.OverLimit {
			for axle := g.StartAxle - 1; axle <= g.EndAxle-1; axle++ {
				retestOver[axle] = true
			}
		}
	}

	changes := make([]OverLimitChange, 0)
	for axle := 0; axle < axleCount; {
		if firstOver[axle] == retestOver[axle] {
			axle++
			continue
		}
		start := axle
		fo, ro := firstOver[axle], retestOver[axle]
		for axle < axleCount && firstOver[axle] == fo && retestOver[axle] == ro {
			axle++
		}
		changes = append(changes, OverLimitChange{
			StartAxle:       start + 1,
			EndAxle:         axle,
			FirstOverLimit:  fo,
			RetestOverLimit: ro,
		})
	}
	return changes
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// evaluate 对已校验的输入执行分组与裁决，axleLoadsKg 为实际参与裁决的载荷
// （可能是地磅校准后的值），不再重复校验载荷范围。
func evaluate(axleLoadsKg []int, axleSpacingsMm []int) (*Result, error) {
	spans := splitGroups(len(axleLoadsKg), axleSpacingsMm)
	groups := make([]GroupResult, 0, len(spans))
	violations := make([]Violation, 0)
	vehicleLoad := 0

	for gi, span := range spans {
		count := span.end - span.start + 1
		if count >= 4 {
			return nil, fmt.Errorf("第 %d 轴组包含 %d 轴（首尾轴为第 %d、%d 轴），形成四轴及以上轴组时输入非法",
				gi+1, count, span.start+1, span.end+1)
		}
		load := 0
		for ax := span.start; ax <= span.end; ax++ {
			load += axleLoadsKg[ax]
		}
		limit := groupLimitKg[count]
		over := load > limit // 等于限值合规
		groups = append(groups, GroupResult{
			Index:     gi + 1,
			StartAxle: span.start + 1,
			EndAxle:   span.end + 1,
			AxleCount: count,
			LoadKg:    load,
			LimitKg:   limit,
			OverLimit: over,
		})
		if over {
			violations = append(violations, Violation{Scope: "group", Index: gi + 1})
		}
		vehicleLoad += load
	}

	vehicle := VehicleResult{
		LoadKg:    vehicleLoad,
		LimitKg:   VehicleLimitKg,
		OverLimit: vehicleLoad > VehicleLimitKg,
	}
	if vehicle.OverLimit {
		violations = append(violations, Violation{Scope: "vehicle"})
	}

	return &Result{Groups: groups, Vehicle: vehicle, Violations: violations}, nil
}
