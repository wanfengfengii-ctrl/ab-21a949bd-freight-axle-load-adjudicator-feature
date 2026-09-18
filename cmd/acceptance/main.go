// Command acceptance 是一次性黑盒验收程序：等待服务就绪后，对运行中的 API
// 执行 1800/1801 毫米临界两侧、非法输入 422、可重复性、地磅校准以及重测比对
// （两次一致确认、载荷翻转、轴距重排、第二份非法无部分结果）检查，
// 并校验 JSON 请求契约（非 JSON 媒体类型拒绝、字段名大小写变体按未知字段拒绝），
// 再以三轴车辆验收桥面承载窗口分析（边界恰好容纳前后轴计入载荷、平移后峰值
// 触发拦停、并列峰值选择最早事件、位置重复只返回错误信封、极大轴位置与载荷
// 精确计算不溢出），
// 全部通过才以 0 退出。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const overallTimeout = 45 * time.Second

func main() {
	base := envOr("BASE_URL", "http://localhost:8080")
	ctx, cancel := context.WithTimeout(context.Background(), overallTimeout)
	defer cancel()

	failures := 0
	check := func(name string, fn func() error) {
		if err := fn(); err != nil {
			fmt.Printf("[FAIL] %s: %v\n", name, err)
			failures++
			return
		}
		fmt.Printf("[PASS] %s\n", name)
	}

	fmt.Printf("等待 API 就绪: %s/healthz ...\n", base)
	if err := waitReady(ctx, base+"/healthz"); err != nil {
		fmt.Printf("服务在 %s 内未就绪: %v\n", overallTimeout, err)
		os.Exit(1)
	}
	fmt.Println("服务已就绪")

	client := &http.Client{Timeout: 10 * time.Second}

	// 1800 毫米：<=1800 同组，双轴组 19000 > 18000，组超限、整车不超限。
	check("轴距 1800mm 两轴同组且双轴组超限", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{"axle_loads_kg": []int{9500, 9500}, "axle_spacings_mm": []int{1800}})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			Groups []struct {
				Index     int  `json:"index"`
				StartAxle int  `json:"start_axle"`
				EndAxle   int  `json:"end_axle"`
				AxleCount int  `json:"axle_count"`
				LoadKg    int  `json:"load_kg"`
				LimitKg   int  `json:"limit_kg"`
				OverLimit bool `json:"over_limit"`
			} `json:"groups"`
			Vehicle struct {
				LoadKg    int  `json:"load_kg"`
				LimitKg   int  `json:"limit_kg"`
				OverLimit bool `json:"over_limit"`
			} `json:"vehicle"`
			Violations []map[string]any `json:"violations"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if len(got.Groups) != 1 {
			return fmt.Errorf("期望 1 个轴组，实际 %d", len(got.Groups))
		}
		g := got.Groups[0]
		if g != (struct {
			Index     int  `json:"index"`
			StartAxle int  `json:"start_axle"`
			EndAxle   int  `json:"end_axle"`
			AxleCount int  `json:"axle_count"`
			LoadKg    int  `json:"load_kg"`
			LimitKg   int  `json:"limit_kg"`
			OverLimit bool `json:"over_limit"`
		}{1, 1, 2, 2, 19000, 18000, true}) {
			return fmt.Errorf("轴组裁决不符: %+v", g)
		}
		if got.Vehicle.LoadKg != 19000 || got.Vehicle.LimitKg != 49000 || got.Vehicle.OverLimit {
			return fmt.Errorf("整车裁决不符: %+v", got.Vehicle)
		}
		if len(got.Violations) != 1 || got.Violations[0]["scope"] != "group" ||
			int(got.Violations[0]["index"].(float64)) != 1 {
			return fmt.Errorf("超限清单不符: %+v", got.Violations)
		}
		return nil
	})

	// 1801 毫米：>1800 开启下一组，两个单轴组各 9500 <= 10000，全部合规。
	check("轴距 1801mm 拆成两个合规单轴组", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{"axle_loads_kg": []int{9500, 9500}, "axle_spacings_mm": []int{1801}})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			Groups []struct {
				StartAxle int  `json:"start_axle"`
				EndAxle   int  `json:"end_axle"`
				AxleCount int  `json:"axle_count"`
				LoadKg    int  `json:"load_kg"`
				LimitKg   int  `json:"limit_kg"`
				OverLimit bool `json:"over_limit"`
			} `json:"groups"`
			Violations []any `json:"violations"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if len(got.Groups) != 2 {
			return fmt.Errorf("期望 2 个轴组，实际 %d", len(got.Groups))
		}
		for i, g := range got.Groups {
			if g.AxleCount != 1 || g.LoadKg != 9500 || g.LimitKg != 10000 || g.OverLimit ||
				g.StartAxle != i+1 || g.EndAxle != i+1 {
				return fmt.Errorf("第 %d 组裁决不符: %+v", i+1, g)
			}
		}
		if len(got.Violations) != 0 {
			return fmt.Errorf("期望无超限，实际 %+v", got.Violations)
		}
		return nil
	})

	// 四轴及以上轴组非法：统一 422 且不得夹带任何部分结果。
	check("形成四轴组时返回 422 且无部分结果", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{
				"axle_loads_kg":    []int{1, 1, 1, 1},
				"axle_spacings_mm": []int{1800, 1800, 1800},
			})
		if err != nil {
			return err
		}
		if code != http.StatusUnprocessableEntity {
			return fmt.Errorf("期望 422，实际 %d，响应 %s", code, body)
		}
		if bytes.Contains(body, []byte("groups")) || bytes.Contains(body, []byte("vehicle")) {
			return fmt.Errorf("422 响应夹带了裁决结果: %s", body)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
			return fmt.Errorf("422 响应缺少 error 字段: %s", body)
		}
		return nil
	})

	// 合法对象后多写右方括号（及其他尾随垃圾）：必须 422 拒绝，不得给出裁决。
	check("合法对象后多余右括号时返回 422", func() error {
		for _, suffix := range []string{"]", "}", "1", ",\n"} {
			raw := []byte(`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}` + suffix)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/verify", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("后缀 %q 请求失败: %w", suffix, err)
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				return fmt.Errorf("后缀 %q 期望 422，实际 %d，响应 %s", suffix, resp.StatusCode, body)
			}
			if bytes.Contains(body, []byte("groups")) {
				return fmt.Errorf("后缀 %q 的 422 夹带了裁决结果: %s", suffix, body)
			}
		}
		return nil
	})

	// 同一请求两次，原始响应必须逐字节一致（可重复、唯一执法结论）。
	check("相同输入两次响应逐字节一致", func() error {
		req := map[string]any{
			"axle_loads_kg":    []int{20000, 20000, 10000},
			"axle_spacings_mm": []int{1801, 1801},
		}
		_, first, err := postJSON(ctx, client, base+"/api/v1/verify", req)
		if err != nil {
			return err
		}
		_, second, err := postJSON(ctx, client, base+"/api/v1/verify", req)
		if err != nil {
			return err
		}
		if !bytes.Equal(first, second) {
			return fmt.Errorf("响应不一致:\n%s\n%s", first, second)
		}
		return nil
	})

	// 地磅校准：同一载荷未校准时双轴组 18400 > 18000 超限；
	// 携带地磅 18000 校准为 [9000,9000] 后等于限值合规，结论翻转。
	check("地磅校准后超限结论翻转", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{"axle_loads_kg": []int{9200, 9200}, "axle_spacings_mm": []int{1800}})
		if err != nil {
			return err
		}
		if code != http.StatusOK || !bytes.Contains(body, []byte(`"over_limit":true`)) {
			return fmt.Errorf("未校准时期望组超限，实际 %d，响应 %s", code, body)
		}

		code, body, err = postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{
				"axle_loads_kg":    []int{9200, 9200},
				"axle_spacings_mm": []int{1800},
				"scale_weight_kg":  18000,
			})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			Groups []struct {
				LoadKg    int  `json:"load_kg"`
				OverLimit bool `json:"over_limit"`
			} `json:"groups"`
			Vehicle struct {
				LoadKg    int  `json:"load_kg"`
				OverLimit bool `json:"over_limit"`
			} `json:"vehicle"`
			Violations  []any `json:"violations"`
			Calibration struct {
				ScaleWeightKg     int   `json:"scale_weight_kg"`
				OriginalTotalKg   int   `json:"original_total_kg"`
				DifferenceKg      int   `json:"difference_kg"`
				CalibratedLoadsKg []int `json:"calibrated_loads_kg"`
			} `json:"calibration"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if len(got.Groups) != 1 || got.Groups[0].LoadKg != 18000 || got.Groups[0].OverLimit {
			return fmt.Errorf("校准后轴组裁决不符: %+v", got.Groups)
		}
		if got.Vehicle.LoadKg != 18000 || got.Vehicle.OverLimit || len(got.Violations) != 0 {
			return fmt.Errorf("校准后整车裁决不符: %+v，超限清单 %+v", got.Vehicle, got.Violations)
		}
		cal := got.Calibration
		if cal.ScaleWeightKg != 18000 || cal.OriginalTotalKg != 18400 || cal.DifferenceKg != -400 ||
			len(cal.CalibratedLoadsKg) != 2 || cal.CalibratedLoadsKg[0] != 9000 || cal.CalibratedLoadsKg[1] != 9000 {
			return fmt.Errorf("校准信息不符: %+v", cal)
		}
		return nil
	})

	// 地磅重量与轴载荷合计偏差超过 5%：422 且不得夹带任何部分结果。
	check("地磅偏差超过 5% 返回 422 且无部分结果", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{
				"axle_loads_kg":    []int{10000, 10000},
				"axle_spacings_mm": []int{1801},
				"scale_weight_kg":  21001,
			})
		if err != nil {
			return err
		}
		if code != http.StatusUnprocessableEntity {
			return fmt.Errorf("期望 422，实际 %d，响应 %s", code, body)
		}
		for _, kw := range []string{"groups", "vehicle", "violations", "calibration"} {
			if bytes.Contains(body, []byte(kw)) {
				return fmt.Errorf("422 响应夹带了部分结果（%s）: %s", kw, body)
			}
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
			return fmt.Errorf("422 响应缺少 error 字段: %s", body)
		}
		return nil
	})

	// 携带地磅重量的同一请求两次，响应同样必须逐字节一致（校准结果可重复）。
	check("相同地磅校准请求两次响应逐字节一致", func() error {
		req := map[string]any{
			"axle_loads_kg":    []int{9200, 9200},
			"axle_spacings_mm": []int{1800},
			"scale_weight_kg":  18000,
		}
		_, first, err := postJSON(ctx, client, base+"/api/v1/verify", req)
		if err != nil {
			return err
		}
		_, second, err := postJSON(ctx, client, base+"/api/v1/verify", req)
		if err != nil {
			return err
		}
		if !bytes.Equal(first, second) {
			return fmt.Errorf("响应不一致:\n%s\n%s", first, second)
		}
		return nil
	})

	// 未携带地磅重量的 1800/1801 请求：响应与校准能力引入前逐字节一致。
	check("未携带地磅重量的 1800/1801 响应逐字节不变", func() error {
		cases := []struct {
			name string
			req  map[string]any
			want string
		}{
			{
				"1800mm",
				map[string]any{"axle_loads_kg": []int{9500, 9500}, "axle_spacings_mm": []int{1800}},
				`{"groups":[{"index":1,"start_axle":1,"end_axle":2,"axle_count":2,"load_kg":19000,"limit_kg":18000,"over_limit":true}],"vehicle":{"load_kg":19000,"limit_kg":49000,"over_limit":false},"violations":[{"scope":"group","index":1}]}`,
			},
			{
				"1801mm",
				map[string]any{"axle_loads_kg": []int{9500, 9500}, "axle_spacings_mm": []int{1801}},
				`{"groups":[{"index":1,"start_axle":1,"end_axle":1,"axle_count":1,"load_kg":9500,"limit_kg":10000,"over_limit":false},{"index":2,"start_axle":2,"end_axle":2,"axle_count":1,"load_kg":9500,"limit_kg":10000,"over_limit":false}],"vehicle":{"load_kg":19000,"limit_kg":49000,"over_limit":false},"violations":[]}`,
			},
		}
		for _, tc := range cases {
			code, body, err := postJSON(ctx, client, base+"/api/v1/verify", tc.req)
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s 期望 200，实际 %d，响应 %s", tc.name, code, body)
			}
			if string(body) != tc.want {
				return fmt.Errorf("%s 响应发生变化:\n期望 %s\n实际 %s", tc.name, tc.want, body)
			}
		}
		return nil
	})

	// ---- 重测比对入口 POST /api/v1/retest-comparison ----

	// 场景一：两次完全一致 -> 结论确认，无任何变化项，且两份完整结果与单次
	// 裁决入口对同一输入的响应逐字节一致（嵌在 first_result/retest_result 下）。
	check("重测比对：两次一致时结论确认且两份结果完整", func() error {
		measurement := map[string]any{
			"axle_loads_kg":    []int{20000, 20000, 10000},
			"axle_spacings_mm": []int{1801, 1801},
		}
		standaloneCode, standalone, err := postJSON(ctx, client, base+"/api/v1/verify", measurement)
		if err != nil {
			return err
		}
		if standaloneCode != http.StatusOK {
			return fmt.Errorf("对照裁决期望 200，实际 %d，响应 %s", standaloneCode, standalone)
		}

		code, body, err := postJSON(ctx, client, base+"/api/v1/retest-comparison",
			map[string]any{"first": measurement, "retest": measurement})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			FirstResult          json.RawMessage `json:"first_result"`
			RetestResult         json.RawMessage `json:"retest_result"`
			GroupBoundaryChanges []any           `json:"group_boundary_changes"`
			OverLimitChanges     []any           `json:"over_limit_changes"`
			VehicleChange        json.RawMessage `json:"vehicle_conclusion_change"`
			Conclusion           string          `json:"conclusion"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if got.Conclusion != "结论确认" {
			return fmt.Errorf("期望结论确认，实际 %q，响应 %s", got.Conclusion, body)
		}
		if len(got.GroupBoundaryChanges) != 0 || len(got.OverLimitChanges) != 0 {
			return fmt.Errorf("两次一致时不应有变化项，响应 %s", body)
		}
		if len(got.VehicleChange) != 0 {
			return fmt.Errorf("整车结论未翻转时不应出现 vehicle_conclusion_change，响应 %s", body)
		}
		if !bytes.Equal(bytes.TrimSpace(got.FirstResult), bytes.TrimSpace(standalone)) ||
			!bytes.Equal(bytes.TrimSpace(got.RetestResult), bytes.TrimSpace(standalone)) {
			return fmt.Errorf("嵌套结果与单次裁决不一致:\n单次 %s\n首次 %s\n重测 %s",
				standalone, got.FirstResult, got.RetestResult)
		}
		if !bytes.Contains(body, []byte(`"first_result":`+string(standalone))) {
			return fmt.Errorf("first_result 未与单次裁决响应逐字节一致:\n单次 %s\n响应 %s", standalone, body)
		}
		return nil
	})

	// 场景二：载荷变化导致超限翻转（分组不变）：1800mm 双轴组 18000 合规 -> 18400 超限。
	check("重测比对：载荷变化导致超限翻转", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/retest-comparison", map[string]any{
			"first": map[string]any{
				"axle_loads_kg":    []int{9000, 9000},
				"axle_spacings_mm": []int{1800},
			},
			"retest": map[string]any{
				"axle_loads_kg":    []int{9200, 9200},
				"axle_spacings_mm": []int{1800},
			},
		})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			GroupBoundaryChanges []any `json:"group_boundary_changes"`
			OverLimitChanges     []struct {
				StartAxle       int  `json:"start_axle"`
				EndAxle         int  `json:"end_axle"`
				FirstOverLimit  bool `json:"first_over_limit"`
				RetestOverLimit bool `json:"retest_over_limit"`
			} `json:"over_limit_changes"`
			VehicleChange json.RawMessage `json:"vehicle_conclusion_change"`
			Conclusion    string          `json:"conclusion"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if got.Conclusion != "结论改变" {
			return fmt.Errorf("期望结论改变，实际 %q", got.Conclusion)
		}
		if len(got.GroupBoundaryChanges) != 0 {
			return fmt.Errorf("轴距未变不应有分组边界变化: %s", body)
		}
		if len(got.OverLimitChanges) != 1 {
			return fmt.Errorf("期望恰好 1 个超限翻转区间，实际 %d: %s", len(got.OverLimitChanges), body)
		}
		oc := got.OverLimitChanges[0]
		if oc.StartAxle != 1 || oc.EndAxle != 2 || oc.FirstOverLimit || !oc.RetestOverLimit {
			return fmt.Errorf("超限翻转区间不符: %+v", oc)
		}
		if len(got.VehicleChange) != 0 {
			return fmt.Errorf("两侧整车均不超限，不应有整车结论变化: %s", body)
		}
		return nil
	})

	// 场景三：轴距变化导致分组重排：1800mm 双轴超限组 -> 1801mm 两个合规单轴组。
	check("重测比对：轴距变化导致分组重排", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/retest-comparison", map[string]any{
			"first": map[string]any{
				"axle_loads_kg":    []int{9500, 9500},
				"axle_spacings_mm": []int{1800},
			},
			"retest": map[string]any{
				"axle_loads_kg":    []int{9500, 9500},
				"axle_spacings_mm": []int{1801},
			},
		})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			GroupBoundaryChanges []struct {
				StartAxle   int `json:"start_axle"`
				EndAxle     int `json:"end_axle"`
				FirstGroups []struct {
					StartAxle int `json:"start_axle"`
					EndAxle   int `json:"end_axle"`
				} `json:"first_groups"`
				RetestGroups []struct {
					StartAxle int `json:"start_axle"`
					EndAxle   int `json:"end_axle"`
				} `json:"retest_groups"`
			} `json:"group_boundary_changes"`
			OverLimitChanges []struct {
				StartAxle       int  `json:"start_axle"`
				EndAxle         int  `json:"end_axle"`
				FirstOverLimit  bool `json:"first_over_limit"`
				RetestOverLimit bool `json:"retest_over_limit"`
			} `json:"over_limit_changes"`
			Conclusion string `json:"conclusion"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if got.Conclusion != "结论改变" {
			return fmt.Errorf("期望结论改变，实际 %q", got.Conclusion)
		}
		if len(got.GroupBoundaryChanges) != 1 {
			return fmt.Errorf("期望 1 个分组边界变化区间，实际 %d: %s", len(got.GroupBoundaryChanges), body)
		}
		bc := got.GroupBoundaryChanges[0]
		if bc.StartAxle != 1 || bc.EndAxle != 2 {
			return fmt.Errorf("边界变化区间应为 1-2，实际 %d-%d", bc.StartAxle, bc.EndAxle)
		}
		if len(bc.FirstGroups) != 1 || bc.FirstGroups[0].StartAxle != 1 || bc.FirstGroups[0].EndAxle != 2 {
			return fmt.Errorf("首次分组覆盖应为 [1,2]，实际 %+v", bc.FirstGroups)
		}
		if len(bc.RetestGroups) != 2 ||
			bc.RetestGroups[0].StartAxle != 1 || bc.RetestGroups[0].EndAxle != 1 ||
			bc.RetestGroups[1].StartAxle != 2 || bc.RetestGroups[1].EndAxle != 2 {
			return fmt.Errorf("重测分组覆盖应为 [1,1][2,2]，实际 %+v", bc.RetestGroups)
		}
		if len(got.OverLimitChanges) != 1 {
			return fmt.Errorf("期望 1 个超限翻转区间，实际 %d", len(got.OverLimitChanges))
		}
		oc := got.OverLimitChanges[0]
		if oc.StartAxle != 1 || oc.EndAxle != 2 || !oc.FirstOverLimit || oc.RetestOverLimit {
			return fmt.Errorf("超限翻转区间应为 1-2 且 超→合规，实际 %+v", oc)
		}
		return nil
	})

	// 场景四：第二份非法（地磅偏差超 5%）-> 整体 422，错误指明重测，且无任何部分结果。
	check("重测比对：第二份非法时 422 且无部分结果", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/retest-comparison", map[string]any{
			"first": map[string]any{
				"axle_loads_kg":    []int{20000, 20000, 10000},
				"axle_spacings_mm": []int{1801, 1801},
			},
			"retest": map[string]any{
				"axle_loads_kg":    []int{10000, 10000},
				"axle_spacings_mm": []int{1801},
				"scale_weight_kg":  21001,
			},
		})
		if err != nil {
			return err
		}
		if code != http.StatusUnprocessableEntity {
			return fmt.Errorf("期望 422，实际 %d，响应 %s", code, body)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
			return fmt.Errorf("422 响应缺少 error 字段: %s", body)
		}
		if !bytes.Contains(body, []byte("重测")) {
			return fmt.Errorf("错误应指出非法的是重测数据，实际 %q", errResp.Error)
		}
		for _, kw := range []string{"first_result", "retest_result", "groups",
			"vehicle", "violations", "calibration", "conclusion"} {
			if bytes.Contains(body, []byte(kw)) {
				return fmt.Errorf("422 响应夹带了部分结果（%s）: %s", kw, body)
			}
		}
		return nil
	})

	// 重测数据填写到一半被截断（外层 JSON 也因此不完整）：仍须明确指出是
	// “重测数据”不完整，而不是笼统报整个请求体不完整，且不夹带任何结果。
	check("重测比对：重测数据截断时错误归因到重测", func() error {
		raw := []byte(`{"first":{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]},` +
			`"retest":{"axle_loads_kg":[1,2]`)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			base+"/api/v1/retest-comparison", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			return fmt.Errorf("期望 422，实际 %d，响应 %s", resp.StatusCode, body)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
			return fmt.Errorf("422 响应缺少 error 字段: %s", body)
		}
		if !bytes.Contains(body, []byte("重测")) || !bytes.Contains(body, []byte("不完整")) {
			return fmt.Errorf("截断错误应明确指出重测数据不完整，实际 %q", errResp.Error)
		}
		for _, kw := range []string{"first_result", "retest_result", "conclusion"} {
			if bytes.Contains(body, []byte(kw)) {
				return fmt.Errorf("422 响应夹带了部分结果（%s）: %s", kw, body)
			}
		}
		return nil
	})

	// 轴数不一致同样整体 422；未知字段在顶层与任一份数据内均被拒绝。
	check("重测比对：轴数不一致与未知字段返回 422", func() error {
		cases := []struct {
			name    string
			payload map[string]any
		}{
			{
				"轴数不一致",
				map[string]any{
					"first":  map[string]any{"axle_loads_kg": []int{1, 2}, "axle_spacings_mm": []int{1000}},
					"retest": map[string]any{"axle_loads_kg": []int{1, 2, 3}, "axle_spacings_mm": []int{1000, 1000}},
				},
			},
			{
				"顶层未知字段",
				map[string]any{
					"first":  map[string]any{"axle_loads_kg": []int{1}, "axle_spacings_mm": []int{}},
					"retest": map[string]any{"axle_loads_kg": []int{1}, "axle_spacings_mm": []int{}},
					"extra":  1,
				},
			},
			{
				"首次数据未知字段",
				map[string]any{
					"first":  map[string]any{"axle_loads_kg": []int{1}, "axle_spacings_mm": []int{}, "extra": 1},
					"retest": map[string]any{"axle_loads_kg": []int{1}, "axle_spacings_mm": []int{}},
				},
			},
		}
		for _, tc := range cases {
			code, body, err := postJSON(ctx, client, base+"/api/v1/retest-comparison", tc.payload)
			if err != nil {
				return fmt.Errorf("%s: %w", tc.name, err)
			}
			if code != http.StatusUnprocessableEntity {
				return fmt.Errorf("%s 期望 422，实际 %d，响应 %s", tc.name, code, body)
			}
			for _, kw := range []string{"first_result", "retest_result", "conclusion"} {
				if bytes.Contains(body, []byte(kw)) {
					return fmt.Errorf("%s 的 422 夹带了部分结果（%s）: %s", tc.name, kw, body)
				}
			}
		}
		return nil
	})

	// 非 JSON 媒体类型（如 text/plain）：即使载荷格式正确，也必须按 JSON 请求契约
	// 整体 422 拒绝，不得生成任何执法结论；两个 POST 入口行为一致。
	check("非 JSON 媒体类型提交被 422 拒绝", func() error {
		verifyBody := `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}`
		bodies := map[string]string{
			"/api/v1/verify":            verifyBody,
			"/api/v1/retest-comparison": `{"first":` + verifyBody + `,"retest":` + verifyBody + `}`,
		}
		for _, path := range []string{"/api/v1/verify", "/api/v1/retest-comparison"} {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path,
				bytes.NewReader([]byte(bodies[path])))
			req.Header.Set("Content-Type", "text/plain")
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("%s 请求失败: %w", path, err)
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				return fmt.Errorf("%s 以 text/plain 提交期望 422，实际 %d，响应 %s", path, resp.StatusCode, body)
			}
			var errResp struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
				return fmt.Errorf("%s 的 422 响应缺少 error 字段: %s", path, body)
			}
			for _, kw := range []string{"groups", "vehicle", "violations",
				"first_result", "retest_result", "conclusion"} {
				if bytes.Contains(body, []byte(kw)) {
					return fmt.Errorf("%s 的 422 响应夹带了裁决结果（%s）: %s", path, kw, body)
				}
			}
		}
		return nil
	})

	// 字段名大小写变体（如全大写 AXLE_LOADS_KG）不符合明确字段契约，
	// 必须按未知字段 422 拒绝；比对入口中错误还须归因到对应那份数据。
	check("字段名大小写变体按未知字段 422 拒绝", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/verify",
			map[string]any{"AXLE_LOADS_KG": []int{9500, 9500}, "axle_spacings_mm": []int{1800}})
		if err != nil {
			return err
		}
		if code != http.StatusUnprocessableEntity {
			return fmt.Errorf("单次裁决全大写字段期望 422，实际 %d，响应 %s", code, body)
		}
		if !bytes.Contains(body, []byte("unknown field")) || bytes.Contains(body, []byte("groups")) {
			return fmt.Errorf("单次裁决全大写字段应按未知字段拒绝且无部分结果: %s", body)
		}

		code, body, err = postJSON(ctx, client, base+"/api/v1/retest-comparison", map[string]any{
			"first":  map[string]any{"axle_loads_kg": []int{9500, 9500}, "axle_spacings_mm": []int{1800}},
			"retest": map[string]any{"AXLE_LOADS_KG": []int{9500, 9500}, "axle_spacings_mm": []int{1800}},
		})
		if err != nil {
			return err
		}
		if code != http.StatusUnprocessableEntity {
			return fmt.Errorf("重测数据全大写字段期望 422，实际 %d，响应 %s", code, body)
		}
		if !bytes.Contains(body, []byte("重测")) || !bytes.Contains(body, []byte("unknown field")) {
			return fmt.Errorf("错误应指出重测数据的未知字段，实际 %s", body)
		}
		for _, kw := range []string{"first_result", "retest_result", "conclusion"} {
			if bytes.Contains(body, []byte(kw)) {
				return fmt.Errorf("422 响应夹带了部分结果（%s）: %s", kw, body)
			}
		}
		return nil
	})

	// ---- 桥面承载窗口分析入口 POST /api/v1/bridge-window ----

	// 三轴车、桥面有效长度恰好等于首尾轴距：平移至车头轴抵达桥出口、
	// 车尾轴抵达桥入口的瞬间（位移 3000），边界上的前后轴均计入载荷，
	// 三轴合计 12000 达到峰值；峰值等于核定载荷，判定通行。
	check("桥面窗口：边界恰好容纳前后轴时计入载荷", func() error {
		return checkBridgeWindow(ctx, client, base,
			map[string]any{
				"axle_positions_mm": []int{0, 1500, 3000},
				"axle_loads_kg":     []int{4000, 4000, 4000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  12000,
			},
			bridgeWindowWant{maxLoadKg: "12000", firstAxle: 1, lastAxle: 3,
				displacementMm: 3000, conclusion: "通行"})
	})

	// 同一车辆、核定载荷低于峰值 1 千克：平移后出现的峰值触发拦停。
	check("桥面窗口：平移后峰值触发拦停", func() error {
		return checkBridgeWindow(ctx, client, base,
			map[string]any{
				"axle_positions_mm": []int{0, 1500, 3000},
				"axle_loads_kg":     []int{4000, 4000, 4000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  11999,
			},
			bridgeWindowWant{maxLoadKg: "12000", firstAxle: 1, lastAxle: 3,
				displacementMm: 3000, conclusion: "拦停"})
	})

	// 并列峰值：{2,3} 轴在位移 2000、{1,2} 轴在位移 4000 同为 8000，
	// 按车辆位移最小确定唯一结果（若先比首轴序号会误选位移 4000 的候选）。
	check("桥面窗口：并列峰值选择最早事件", func() error {
		return checkBridgeWindow(ctx, client, base,
			map[string]any{
				"axle_positions_mm": []int{0, 2000, 4000},
				"axle_loads_kg":     []int{5000, 3000, 5000},
				"bridge_length_mm":  2000,
				"approved_load_kg":  8000,
			},
			bridgeWindowWant{maxLoadKg: "8000", firstAxle: 2, lastAxle: 3,
				displacementMm: 2000, conclusion: "通行"})
	})

	// 位置重复：统一 422，只返回错误信封，不生成任何分析结果。
	check("桥面窗口：位置重复时只返回错误信封", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
			map[string]any{
				"axle_positions_mm": []int{0, 2000, 2000},
				"axle_loads_kg":     []int{4000, 4000, 4000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  12000,
			})
		if err != nil {
			return err
		}
		if code != http.StatusUnprocessableEntity {
			return fmt.Errorf("期望 422，实际 %d，响应 %s", code, body)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
			return fmt.Errorf("422 响应缺少 error 字段: %s", body)
		}
		for _, kw := range []string{"max_load_kg", "first_axle", "last_axle",
			"displacement_mm", "conclusion"} {
			if bytes.Contains(body, []byte(kw)) {
				return fmt.Errorf("422 响应夹带了分析结果（%s）: %s", kw, body)
			}
		}
		return nil
	})

	// 同一桥面窗口请求两次，响应必须逐字节一致（唯一确定结论）。
	check("桥面窗口：相同输入两次响应逐字节一致", func() error {
		req := map[string]any{
			"axle_positions_mm": []int{0, 2000, 4000},
			"axle_loads_kg":     []int{5000, 3000, 5000},
			"bridge_length_mm":  2000,
			"approved_load_kg":  8000,
		}
		_, first, err := postJSON(ctx, client, base+"/api/v1/bridge-window", req)
		if err != nil {
			return err
		}
		_, second, err := postJSON(ctx, client, base+"/api/v1/bridge-window", req)
		if err != nil {
			return err
		}
		if !bytes.Equal(first, second) {
			return fmt.Errorf("响应不一致:\n%s\n%s", first, second)
		}
		return nil
	})

	// 省略预计车速时，桥面窗口响应与引入车速能力前逐字节一致：原五字段响应
	// 原样返回，绝不夹带 overload_segments / total_overload_duration_ms。
	check("桥面窗口：省略车速时既有响应字节不变", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
			map[string]any{
				"axle_positions_mm": []int{0, 1500, 3000},
				"axle_loads_kg":     []int{4000, 4000, 4000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  12000,
			})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		want := `{"max_load_kg":12000,"first_axle":1,"last_axle":3,` +
			`"displacement_mm":3000,"conclusion":"通行"}`
		if string(body) != want {
			return fmt.Errorf("省略车速响应发生变化:\n期望 %s\n实际 %s", want, body)
		}
		if bytes.Contains(body, []byte("overload_segments")) ||
			bytes.Contains(body, []byte("total_overload_duration_ms")) {
			return fmt.Errorf("省略车速时不得返回区段字段: %s", body)
		}

		// 显式 null 与省略完全等价，响应同样逐字节不变。
		code, bodyNull, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
			map[string]any{
				"axle_positions_mm": []int{0, 1500, 3000},
				"axle_loads_kg":     []int{4000, 4000, 4000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  12000,
				"speed_mm_per_s":    nil,
			})
		if err != nil {
			return err
		}
		if code != http.StatusOK || !bytes.Equal(body, bodyNull) {
			return fmt.Errorf("speed_mm_per_s=null 应与省略逐字节一致:\n%s\n%s", body, bodyNull)
		}
		return nil
	})

	// 持续超载：两轴各 7000、核定 10000，两轴同时落桥期间形成唯一一段
	// 恒定载荷 14000 的超载区段 [1000,3000)；车速 3000 毫米每秒时
	// 2000×1000÷3000 = 666.66… 向上取整为 667 毫秒。原峰值字段不变。
	check("桥面窗口：持续超载区段载荷与向上取整时长", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
			map[string]any{
				"axle_positions_mm": []int{0, 1000},
				"axle_loads_kg":     []int{7000, 7000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  10000,
				"speed_mm_per_s":    3000,
			})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			MaxLoadKg        json.Number `json:"max_load_kg"`
			FirstAxle        int         `json:"first_axle"`
			LastAxle         int         `json:"last_axle"`
			DisplacementMm   int         `json:"displacement_mm"`
			Conclusion       string      `json:"conclusion"`
			OverloadSegments []struct {
				StartDisplacementMm int         `json:"start_displacement_mm"`
				EndDisplacementMm   int         `json:"end_displacement_mm"`
				LoadKg              json.Number `json:"load_kg"`
				DurationMs          int         `json:"duration_ms"`
			} `json:"overload_segments"`
			TotalOverloadDurationMs int `json:"total_overload_duration_ms"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if got.MaxLoadKg.String() != "14000" || got.FirstAxle != 1 || got.LastAxle != 2 ||
			got.DisplacementMm != 1000 || got.Conclusion != "拦停" {
			return fmt.Errorf("原峰值字段与不带车速时不符: %s", body)
		}
		if len(got.OverloadSegments) != 1 {
			return fmt.Errorf("期望恰好 1 个超载区段，实际 %d: %s",
				len(got.OverloadSegments), body)
		}
		s := got.OverloadSegments[0]
		if s.StartDisplacementMm != 1000 || s.EndDisplacementMm != 3000 ||
			s.LoadKg.String() != "14000" || s.DurationMs != 667 {
			return fmt.Errorf("超载区段不符: %+v", s)
		}
		if got.TotalOverloadDurationMs != 667 {
			return fmt.Errorf("累计超载时长应为 667，实际 %d", got.TotalOverloadDurationMs)
		}
		return nil
	})

	// 载荷变化：平移过程中恒定载荷依次为 6000/7000/13000/7000/6000，
	// 相邻区段载荷互不相等，必须逐段返回而不得合并，累计时长为五段之和。
	check("桥面窗口：不同恒定载荷区段不合并", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
			map[string]any{
				"axle_positions_mm": []int{0, 1000, 2000},
				"axle_loads_kg":     []int{6000, 1000, 6000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  5000,
				"speed_mm_per_s":    1000,
			})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			MaxLoadKg        json.Number `json:"max_load_kg"`
			Conclusion       string      `json:"conclusion"`
			OverloadSegments []struct {
				StartDisplacementMm int         `json:"start_displacement_mm"`
				EndDisplacementMm   int         `json:"end_displacement_mm"`
				LoadKg              json.Number `json:"load_kg"`
				DurationMs          int         `json:"duration_ms"`
			} `json:"overload_segments"`
			TotalOverloadDurationMs int `json:"total_overload_duration_ms"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		wantSegs := []struct {
			start, end, duration int
			load                 string
		}{
			{0, 1000, 1000, "6000"},
			{1000, 2000, 1000, "7000"},
			{2000, 3000, 1000, "13000"},
			{3000, 4000, 1000, "7000"},
			{4000, 5000, 1000, "6000"},
		}
		if len(got.OverloadSegments) != len(wantSegs) {
			return fmt.Errorf("期望 %d 个不合并区段，实际 %d: %s",
				len(wantSegs), len(got.OverloadSegments), body)
		}
		for i, want := range wantSegs {
			s := got.OverloadSegments[i]
			if s.StartDisplacementMm != want.start || s.EndDisplacementMm != want.end ||
				s.LoadKg.String() != want.load || s.DurationMs != want.duration {
				return fmt.Errorf("第 %d 区段不符: 期望 %+v，实际 %+v", i+1, want, s)
			}
		}
		if got.MaxLoadKg.String() != "13000" || got.Conclusion != "拦停" {
			return fmt.Errorf("原峰值裁决字段不符: %s", body)
		}
		if got.TotalOverloadDurationMs != 5000 {
			return fmt.Errorf("累计超载时长应为 5000，实际 %d", got.TotalOverloadDurationMs)
		}
		return nil
	})

	// 同位移处进入先于离开：位移 3000 的瞬时三轴峰值 12000 超过核定值 11000，
	// 原峰值仍裁决拦停；但该峰值位移长度为零，不进入区段、不计累计，
	// overload_segments 为空、total_overload_duration_ms 为 0。
	check("桥面窗口：瞬时超载仅影响原峰值且累计为零", func() error {
		code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
			map[string]any{
				"axle_positions_mm": []int{0, 1500, 3000},
				"axle_loads_kg":     []int{4000, 4000, 4000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  11000,
				"speed_mm_per_s":    1000,
			})
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
		}
		var got struct {
			MaxLoadKg               json.Number `json:"max_load_kg"`
			DisplacementMm          int         `json:"displacement_mm"`
			Conclusion              string      `json:"conclusion"`
			OverloadSegments        []any       `json:"overload_segments"`
			TotalOverloadDurationMs int         `json:"total_overload_duration_ms"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			return err
		}
		if got.MaxLoadKg.String() != "12000" || got.DisplacementMm != 3000 ||
			got.Conclusion != "拦停" {
			return fmt.Errorf("瞬时峰值仍应参与原峰值裁决并拦停: %s", body)
		}
		if len(got.OverloadSegments) != 0 {
			return fmt.Errorf("瞬时超载不得进入区段，实际 %d 个: %s",
				len(got.OverloadSegments), body)
		}
		if got.TotalOverloadDurationMs != 0 {
			return fmt.Errorf("瞬时超载累计时长应为 0，实际 %d", got.TotalOverloadDurationMs)
		}
		return nil
	})

	// 预计车速为零、越界或类型错误：统一 422 错误信封，绝不附带分析结果；
	// 边界值 1 与 50000 合法。
	check("桥面窗口：车速非法时 422 且无分析结果", func() error {
		for _, speed := range []any{0, -1, 50001, "1000", true, []int{1000}} {
			code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
				map[string]any{
					"axle_positions_mm": []int{0, 1000},
					"axle_loads_kg":     []int{7000, 7000},
					"bridge_length_mm":  3000,
					"approved_load_kg":  10000,
					"speed_mm_per_s":    speed,
				})
			if err != nil {
				return err
			}
			if code != http.StatusUnprocessableEntity {
				return fmt.Errorf("车速 %v 期望 422，实际 %d，响应 %s", speed, code, body)
			}
			var errResp struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == "" {
				return fmt.Errorf("车速 %v 的 422 响应缺少 error 字段: %s", speed, body)
			}
			for _, kw := range []string{"max_load_kg", "conclusion",
				"overload_segments", "total_overload_duration_ms"} {
				if bytes.Contains(body, []byte(kw)) {
					return fmt.Errorf("车速 %v 的 422 响应夹带了分析结果（%s）: %s",
						speed, kw, body)
				}
			}
		}
		for _, speed := range []int{1, 50000} {
			code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window",
				map[string]any{
					"axle_positions_mm": []int{0},
					"axle_loads_kg":     []int{1},
					"bridge_length_mm":  1000,
					"approved_load_kg":  1,
					"speed_mm_per_s":    speed,
				})
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("边界车速 %d 应合法，实际 %d，响应 %s", speed, code, body)
			}
		}
		return nil
	})

	// 极大轴位置与载荷不设上限、不得溢出：极大载荷合计须作为 JSON 数字
	// 精确返回（不得报成零或少算），极大位置下离开位移溢出仍须精确分析。
	check("桥面窗口：极大轴位置与载荷精确计算不溢出", func() error {
		// 三轴各 2^63-1：峰值 3×9223372036854775807 = 27670116110564327421。
		if err := checkBridgeWindow(ctx, client, base,
			map[string]any{
				"axle_positions_mm": []int{0, 1500, 3000},
				"axle_loads_kg":     []int{9223372036854775807, 9223372036854775807, 9223372036854775807},
				"bridge_length_mm":  3000,
				"approved_load_kg":  200000,
			},
			bridgeWindowWant{maxLoadKg: "27670116110564327421", firstAxle: 1, lastAxle: 3,
				displacementMm: 3000, conclusion: "拦停"}); err != nil {
			return fmt.Errorf("极大载荷: %w", err)
		}
		// 后轴位置 2^63-1：其离开位移超出 int 范围，两轴不会同时落桥。
		if err := checkBridgeWindow(ctx, client, base,
			map[string]any{
				"axle_positions_mm": []int{0, 9223372036854775807},
				"axle_loads_kg":     []int{4000, 5000},
				"bridge_length_mm":  3000,
				"approved_load_kg":  12000,
			},
			bridgeWindowWant{maxLoadKg: "5000", firstAxle: 2, lastAxle: 2,
				displacementMm: 0, conclusion: "通行"}); err != nil {
			return fmt.Errorf("极大位置: %w", err)
		}
		return nil
	})

	if failures > 0 {
		fmt.Printf("\n验收未通过：%d 项失败\n", failures)
		os.Exit(1)
	}
	fmt.Println("\n全部验收项通过")
}

// bridgeWindowWant 为桥面承载窗口分析验收的期望值。最大载荷按十进制字符串
// 比较：极大载荷合计可能超出 int64，须按任意精度精确校验。
type bridgeWindowWant struct {
	maxLoadKg      string
	firstAxle      int
	lastAxle       int
	displacementMm int
	conclusion     string
}

// checkBridgeWindow 提交一次桥面承载窗口分析并逐项校验分析结果。
func checkBridgeWindow(ctx context.Context, client *http.Client, base string,
	payload map[string]any, want bridgeWindowWant) error {
	code, body, err := postJSON(ctx, client, base+"/api/v1/bridge-window", payload)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("期望 200，实际 %d，响应 %s", code, body)
	}
	var got struct {
		MaxLoadKg      json.Number `json:"max_load_kg"`
		FirstAxle      int         `json:"first_axle"`
		LastAxle       int         `json:"last_axle"`
		DisplacementMm int         `json:"displacement_mm"`
		Conclusion     string      `json:"conclusion"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return err
	}
	if got.MaxLoadKg.String() != want.maxLoadKg || got.FirstAxle != want.firstAxle ||
		got.LastAxle != want.lastAxle || got.DisplacementMm != want.displacementMm ||
		got.Conclusion != want.conclusion {
		return fmt.Errorf("分析结果不符: 期望 %+v，实际 %+v", want, got)
	}
	return nil
}

func waitReady(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func postJSON(ctx context.Context, client *http.Client, url string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
