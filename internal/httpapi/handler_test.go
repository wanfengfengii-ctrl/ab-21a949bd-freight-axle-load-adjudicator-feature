package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"axleverify/internal/verify"
)

func newRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r)
	return r
}

func doVerify(t *testing.T, r *gin.Engine, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/verify", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	var out map[string]any
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), "响应应为 JSON: %s", w.Body.String())
	}
	return w.Code, out
}

func doCompare(t *testing.T, r *gin.Engine, body string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/retest-comparison", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func mustJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out), "响应应为 JSON: %s", string(raw))
	return out
}

// resultJSON 调用单次裁决入口并返回原始响应体，供比对响应里的完整结果做逐字节对照。
func resultJSON(t *testing.T, r *gin.Engine, measurement string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/verify", bytes.NewBufferString(measurement))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "对照裁决应成功: %s", w.Body.String())
	return w.Body.Bytes()
}

func TestVerify_Boundary1800vs1801(t *testing.T) {
	r := newRouter(t)

	code, out := doVerify(t, r, `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}`)
	require.Equal(t, http.StatusOK, code)
	groups := out["groups"].([]any)
	require.Len(t, groups, 1)
	g0 := groups[0].(map[string]any)
	assert.Equal(t, float64(2), g0["axle_count"])
	assert.Equal(t, float64(19000), g0["load_kg"])
	assert.Equal(t, float64(18000), g0["limit_kg"])
	assert.Equal(t, true, g0["over_limit"])
	assert.Equal(t, float64(1), g0["start_axle"])
	assert.Equal(t, float64(2), g0["end_axle"])
	violations := out["violations"].([]any)
	require.Len(t, violations, 1)
	assert.Equal(t, "group", violations[0].(map[string]any)["scope"])

	code, out = doVerify(t, r, `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, out["groups"].([]any), 2)
	assert.Empty(t, out["violations"])
	vehicle := out["vehicle"].(map[string]any)
	assert.Equal(t, float64(19000), vehicle["load_kg"])
	assert.Equal(t, float64(49000), vehicle["limit_kg"])
	assert.Equal(t, false, vehicle["over_limit"])
}

func TestVerify_GroupAndVehicleViolationsOrdered(t *testing.T) {
	r := newRouter(t)
	// 三个单轴组，前两组超限且整车 50000 超限；组按序号排列，整车最后。
	code, out := doVerify(t, r, `{"axle_loads_kg":[20000,20000,10000],"axle_spacings_mm":[1801,1801]}`)
	require.Equal(t, http.StatusOK, code)
	got := out["violations"].([]any)
	require.Len(t, got, 3)
	assert.Equal(t, map[string]any{"scope": "group", "index": float64(1)}, got[0])
	assert.Equal(t, map[string]any{"scope": "group", "index": float64(2)}, got[1])
	assert.Equal(t, map[string]any{"scope": "vehicle"}, got[2])
}

func TestVerify_422Cases(t *testing.T) {
	r := newRouter(t)
	cases := []struct {
		name string
		body string
	}{
		{"空体", ``},
		{"语法错误", `{"axle_loads_kg":[1,2],`},
		{"缺载荷字段", `{"axle_spacings_mm":[1000]}`},
		{"缺轴距字段", `{"axle_loads_kg":[1,2]}`},
		{"载荷非数组", `{"axle_loads_kg":100,"axle_spacings_mm":[]}`},
		{"元素非整数", `{"axle_loads_kg":[1.5,2],"axle_spacings_mm":[1000]}`},
		{"元素为字符串", `{"axle_loads_kg":["1",2],"axle_spacings_mm":[1000]}`},
		{"未知字段", `{"axle_loads_kg":[1,2],"axle_spacings_mm":[1000],"extra":1}`},
		{"轴数 0", `{"axle_loads_kg":[],"axle_spacings_mm":[]}`},
		{"轴数 13", `{"axle_loads_kg":[1,1,1,1,1,1,1,1,1,1,1,1,1],"axle_spacings_mm":[1000,1000,1000,1000,1000,1000,1000,1000,1000,1000,1000,1000]}`},
		{"轴距数量不符", `{"axle_loads_kg":[1,2,3],"axle_spacings_mm":[1000]}`},
		{"载荷超上限", `{"axle_loads_kg":[20001],"axle_spacings_mm":[]}`},
		{"轴距超上限", `{"axle_loads_kg":[1,1],"axle_spacings_mm":[10001]}`},
		{"轴距低于下限", `{"axle_loads_kg":[1,1],"axle_spacings_mm":[499]}`},
		{"四轴组非法", `{"axle_loads_kg":[1,1,1,1],"axle_spacings_mm":[1800,1800,1800]}`},
		{"两个 JSON 文档", `{"axle_loads_kg":[1],"axle_spacings_mm":[]}{}`},
		{"合法对象后多余右方括号", `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}]`},
		{"合法对象后多余右花括号", `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}}`},
		{"合法对象后多余数字", `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}1`},
		{"合法对象后多余字符串", `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}"x"`},
		{"合法对象后多余逗号", `{"axle_loads_kg":[1],"axle_spacings_mm":[]},`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := doVerify(t, r, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, code)
			assert.Contains(t, out, "error")
			// 统一 422：绝不能夹带任何裁决结果。
			assert.NotContains(t, out, "groups")
			assert.NotContains(t, out, "vehicle")
			assert.NotContains(t, out, "violations")
		})
	}
}

func TestVerify_TrailingWhitespaceAccepted(t *testing.T) {
	r := newRouter(t)
	for _, body := range []string{
		`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`,
		`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}` + "\n",
		`  {"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}  ` + "\r\n\t",
	} {
		code, _ := doVerify(t, r, body)
		assert.Equal(t, http.StatusOK, code, "仅尾随空白必须接受: %q", body)
	}
}

func TestVerify_NullFieldsAre422(t *testing.T) {
	r := newRouter(t)
	for _, body := range []string{
		`{"axle_loads_kg":null,"axle_spacings_mm":[]}`,
		`{"axle_loads_kg":[1],"axle_spacings_mm":null}`,
	} {
		code, out := doVerify(t, r, body)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Contains(t, out["error"], "缺少必填字段")
	}
}

func TestHealthz(t *testing.T) {
	r := newRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}

// 与领域层常量保持一致，避免魔法数字漂移。
func TestLimitsMatchDomain(t *testing.T) {
	assert.Equal(t, 49000, verify.VehicleLimitKg)
}

func TestVerify_ScaleWeightCalibratesAndFlipsVerdict(t *testing.T) {
	r := newRouter(t)

	// 不带地磅重量：双轴组 18400 > 18000 超限，且无 calibration 字段。
	code, out := doVerify(t, r, `{"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800]}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, out["groups"].([]any)[0].(map[string]any)["over_limit"])
	assert.NotContains(t, out, "calibration")

	// 携带地磅重量 18000：校准为 [9000,9000]，组载荷等于限值合规，结论翻转。
	code, out = doVerify(t, r, `{"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800],"scale_weight_kg":18000}`)
	require.Equal(t, http.StatusOK, code)
	cal, ok := out["calibration"].(map[string]any)
	require.True(t, ok, "携带地磅重量的响应必须含 calibration: %v", out)
	assert.Equal(t, float64(18000), cal["scale_weight_kg"])
	assert.Equal(t, float64(18400), cal["original_total_kg"])
	assert.Equal(t, float64(-400), cal["difference_kg"])
	assert.Equal(t, []any{float64(9000), float64(9000)}, cal["calibrated_loads_kg"])
	g0 := out["groups"].([]any)[0].(map[string]any)
	assert.Equal(t, float64(18000), g0["load_kg"])
	assert.Equal(t, false, g0["over_limit"])
	vehicle := out["vehicle"].(map[string]any)
	assert.Equal(t, float64(18000), vehicle["load_kg"])
	assert.Equal(t, false, vehicle["over_limit"])
	assert.Empty(t, out["violations"])
}

func TestVerify_ScaleWeightDeviationOver5PercentIs422(t *testing.T) {
	r := newRouter(t)
	// 合计 20000，地磅 21001 偏差 1001 > 5%：422 且只说明偏差超界，无任何部分结果。
	code, out := doVerify(t, r, `{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":21001}`)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	require.Contains(t, out, "error")
	assert.Contains(t, out["error"], "偏差超过")
	assert.NotContains(t, out, "groups")
	assert.NotContains(t, out, "vehicle")
	assert.NotContains(t, out, "violations")
	assert.NotContains(t, out, "calibration")

	// 恰好 5% 边界允许。
	code, out = doVerify(t, r, `{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":21000}`)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, out, "calibration")
}

func TestVerify_ScaleWeightInvalidIs422(t *testing.T) {
	r := newRouter(t)
	for _, body := range []string{
		`{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":0}`,
		`{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":-5}`,
		`{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":240001}`,
		`{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":1.5}`,
		`{"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":"18000"}`,
	} {
		code, out := doVerify(t, r, body)
		assert.Equal(t, http.StatusUnprocessableEntity, code, body)
		assert.Contains(t, out, "error")
		assert.NotContains(t, out, "groups")
		assert.NotContains(t, out, "calibration")
	}
}

func TestVerify_NullScaleWeightTreatedAsAbsent(t *testing.T) {
	r := newRouter(t)
	code, out := doVerify(t, r, `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800],"scale_weight_kg":null}`)
	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, out, "calibration")
}

// 未携带 scale_weight_kg 的响应必须与引入校准能力前逐字节一致。
func TestVerify_NoScaleWeightResponseByteIdentical(t *testing.T) {
	r := newRouter(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"1800mm 同组超限",
			`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}`,
			`{"groups":[{"index":1,"start_axle":1,"end_axle":2,"axle_count":2,"load_kg":19000,"limit_kg":18000,"over_limit":true}],"vehicle":{"load_kg":19000,"limit_kg":49000,"over_limit":false},"violations":[{"scope":"group","index":1}]}`,
		},
		{
			"1801mm 拆组合规",
			`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`,
			`{"groups":[{"index":1,"start_axle":1,"end_axle":1,"axle_count":1,"load_kg":9500,"limit_kg":10000,"over_limit":false},{"index":2,"start_axle":2,"end_axle":2,"axle_count":1,"load_kg":9500,"limit_kg":10000,"over_limit":false}],"vehicle":{"load_kg":19000,"limit_kg":49000,"over_limit":false},"violations":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/verify", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tc.want, w.Body.String())
		})
	}
}

// ---------------------------------------------------------------------------
// 重测比对入口 POST /api/v1/retest-comparison
// ---------------------------------------------------------------------------

// 同一请求两次一致：结论确认，无任何变化项，且两份嵌套完整结果与各自单独调用
// /api/v1/verify 的响应逐字节相同（同一结构体经同一 JSON 链路序列化）。
func TestComparison_IdenticalMeasurementsConfirmed(t *testing.T) {
	r := newRouter(t)
	firstBody := `{"axle_loads_kg":[20000,20000,10000],"axle_spacings_mm":[1801,1801]}`
	body := `{"first":` + firstBody + `,"retest":` + firstBody + `}`

	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)
	assert.Equal(t, "结论确认", out["conclusion"])
	assert.JSONEq(t, "[]", toJSON(t, out["group_boundary_changes"]))
	assert.JSONEq(t, "[]", toJSON(t, out["over_limit_changes"]))
	assert.NotContains(t, out, "vehicle_conclusion_change")

	standalone := resultJSON(t, r, firstBody)
	assert.Contains(t, string(raw), `"first_result":`+string(standalone))
	assert.Contains(t, string(raw), `"retest_result":`+string(standalone))
}

// 载荷变化导致超限翻转：分组边界不变，仅轴覆盖区间超限状态翻转。
func TestComparison_LoadChangeFlipsOverLimit(t *testing.T) {
	r := newRouter(t)
	body := `{
		"first":  {"axle_loads_kg":[9000,9000],"axle_spacings_mm":[1800]},
		"retest": {"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800]}
	}`
	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)

	assert.Equal(t, "结论改变", out["conclusion"])
	assert.JSONEq(t, "[]", toJSON(t, out["group_boundary_changes"]))
	assert.NotContains(t, out, "vehicle_conclusion_change")

	changes := out["over_limit_changes"].([]any)
	require.Len(t, changes, 1)
	ch := changes[0].(map[string]any)
	assert.Equal(t, float64(1), ch["start_axle"])
	assert.Equal(t, float64(2), ch["end_axle"])
	assert.Equal(t, false, ch["first_over_limit"])
	assert.Equal(t, true, ch["retest_over_limit"])

	// 两份完整裁决均在响应中。
	fr := out["first_result"].(map[string]any)
	rr := out["retest_result"].(map[string]any)
	assert.Equal(t, false, fr["groups"].([]any)[0].(map[string]any)["over_limit"])
	assert.Equal(t, true, rr["groups"].([]any)[0].(map[string]any)["over_limit"])
}

// 轴距变化导致分组重排：1800mm 双轴超限组 vs 1801mm 两个合规单轴组。
func TestComparison_SpacingChangeRegroups(t *testing.T) {
	r := newRouter(t)
	body := `{
		"first":  {"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]},
		"retest": {"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}
	}`
	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)

	assert.Equal(t, "结论改变", out["conclusion"])

	bcs := out["group_boundary_changes"].([]any)
	require.Len(t, bcs, 1)
	bc := bcs[0].(map[string]any)
	assert.Equal(t, float64(1), bc["start_axle"])
	assert.Equal(t, float64(2), bc["end_axle"])
	assert.JSONEq(t, `[{"start_axle":1,"end_axle":2}]`, toJSON(t, bc["first_groups"]))
	assert.JSONEq(t, `[{"start_axle":1,"end_axle":1},{"start_axle":2,"end_axle":2}]`,
		toJSON(t, bc["retest_groups"]))

	ocs := out["over_limit_changes"].([]any)
	require.Len(t, ocs, 1)
	oc := ocs[0].(map[string]any)
	assert.Equal(t, float64(1), oc["start_axle"])
	assert.Equal(t, float64(2), oc["end_axle"])
	assert.Equal(t, true, oc["first_over_limit"])
	assert.Equal(t, false, oc["retest_over_limit"])
	assert.NotContains(t, out, "vehicle_conclusion_change")
}

// 整车结论翻转：各轴组始终合规，仅整车 49050→49000 从超限变为合规。
func TestComparison_VehicleConclusionFlip(t *testing.T) {
	r := newRouter(t)
	body := `{
		"first":  {"axle_loads_kg":[9810,9810,9810,9810,9810],"axle_spacings_mm":[1801,1801,1801,1801]},
		"retest": {"axle_loads_kg":[9800,9800,9800,9800,9800],"axle_spacings_mm":[1801,1801,1801,1801]}
	}`
	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)

	assert.Equal(t, "结论改变", out["conclusion"])
	assert.JSONEq(t, "[]", toJSON(t, out["group_boundary_changes"]))
	assert.JSONEq(t, "[]", toJSON(t, out["over_limit_changes"]))
	vc := out["vehicle_conclusion_change"].(map[string]any)
	assert.Equal(t, float64(49050), vc["first_load_kg"])
	assert.Equal(t, float64(49000), vc["retest_load_kg"])
	assert.Equal(t, true, vc["first_over_limit"])
	assert.Equal(t, false, vc["retest_over_limit"])
}

// 选填地磅重量沿用既有契约：首次不校准超限、重测携带地磅校准后合规，
// 仅重测结果携带 calibration。
func TestComparison_OptionalScaleWeight(t *testing.T) {
	r := newRouter(t)
	body := `{
		"first":  {"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800]},
		"retest": {"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800],"scale_weight_kg":18000}
	}`
	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)

	assert.Equal(t, "结论改变", out["conclusion"])
	fr := out["first_result"].(map[string]any)
	rr := out["retest_result"].(map[string]any)
	assert.NotContains(t, fr, "calibration")
	require.Contains(t, rr, "calibration")
	assert.Equal(t, []any{float64(9000), float64(9000)},
		rr["calibration"].(map[string]any)["calibrated_loads_kg"])

	// 嵌套的重测结果仍与单次裁决入口（含 calibration）逐字节相同。
	standalone := resultJSON(t, r,
		`{"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800],"scale_weight_kg":18000}`)
	assert.Contains(t, string(raw), `"retest_result":`+string(standalone))
}

func TestComparison_422Cases(t *testing.T) {
	r := newRouter(t)
	valid := `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`
	cases := []struct {
		name      string
		body      string
		wantInErr string
	}{
		{"空体", ``, ""},
		{"顶层语法错误", `{"first":`, ""},
		{"重测值中途截断", `{"first":` + valid + `,"retest":{"axle_loads_kg":[1,2]`, "重测"},
		{"重测值内语法损坏", `{"first":` + valid + `,"retest":{"axle_loads_kg":[1,]}}`, "重测"},
		{"重测值刚开始即截断", `{"first":` + valid + `,"retest":`, "重测"},
		{"首次值中途截断", `{"first":{"axle_loads_kg":[1`, "首次"},
		{"缺 first", `{"retest":` + valid + `}`, "first"},
		{"缺 retest", `{"first":` + valid + `}`, "retest"},
		{"first 为 null", `{"first":null,"retest":` + valid + `}`, "首次"},
		{"retest 为 null", `{"first":` + valid + `,"retest":null}`, "重测"},
		{"first 为数字", `{"first":1,"retest":` + valid + `}`, "首次"},
		{"retest 为数组", `{"first":` + valid + `,"retest":[1,2]}`, "重测"},
		{"顶层未知字段", `{"first":` + valid + `,"retest":` + valid + `,"extra":1}`, "unknown"},
		{"first 未知字段", `{"first":{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801],"x":1},"retest":` + valid + `}`, "首次"},
		{"retest 未知字段", `{"first":` + valid + `,"retest":{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801],"x":1}}`, "重测"},
		{"first 缺载荷", `{"first":{"axle_spacings_mm":[1801]},"retest":` + valid + `}`, "首次"},
		{"retest 元素非整数", `{"first":` + valid + `,"retest":{"axle_loads_kg":[1.5,2],"axle_spacings_mm":[1801]}}`, "重测"},
		{"first 载荷越界", `{"first":{"axle_loads_kg":[0,1],"axle_spacings_mm":[1000]},"retest":` + valid + `}`, "首次"},
		{"retest 轴距越界", `{"first":` + valid + `,"retest":{"axle_loads_kg":[1,1],"axle_spacings_mm":[10001]}}`, "重测"},
		{"retest 四轴组非法", `{"first":` + valid + `,"retest":{"axle_loads_kg":[1,1,1,1],"axle_spacings_mm":[1800,1800,1800]}}`, "重测"},
		{"轴数不一致", `{"first":{"axle_loads_kg":[1,2],"axle_spacings_mm":[1000]},"retest":{"axle_loads_kg":[1,2,3],"axle_spacings_mm":[1000,1000]}}`, "轴数不一致"},
		{"first 重复键", `{"first":` + valid + `,"first":` + valid + `,"retest":` + valid + `}`, "重复"},
		{"顶层不是对象", `[` + valid + `,` + valid + `]`, ""},
		{"两个 JSON 文档", `{"first":` + valid + `,"retest":` + valid + `}{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := doCompare(t, r, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
			out := mustJSONMap(t, raw)
			require.Contains(t, out, "error")
			if tc.wantInErr != "" {
				assert.Contains(t, out["error"], tc.wantInErr)
			}
			// 整体 422：绝不夹带任何一份裁决结果或比对产物。
			for _, kw := range []string{"first_result", "retest_result", "groups",
				"vehicle", "violations", "calibration", "conclusion",
				"group_boundary_changes", "over_limit_changes"} {
				assert.NotContains(t, string(raw), kw, "422 响应不得出现 %s", kw)
			}
		})
	}
}

// 重测数据填写到一半被截断：错误必须明确指出是重测数据不完整，
// 而不是笼统地报整个请求体不完整；首次截断同理归因首次。
func TestComparison_TruncatedMeasurementNamesTheSide(t *testing.T) {
	r := newRouter(t)
	validFirst := `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`
	cases := []struct {
		name           string
		body           string
		side           string
		wantIncomplete bool
	}{
		{
			"重测对象中途截断",
			`{"first":` + validFirst + `,"retest":{"axle_loads_kg":[1,2]`,
			"重测", true,
		},
		{
			"重测值刚开始即截断",
			`{"first":` + validFirst + `,"retest":`,
			"重测", true,
		},
		{
			"重测值内部语法损坏",
			`{"first":` + validFirst + `,"retest":{"axle_loads_kg":[1,]}}`,
			"重测", false,
		},
		{
			"首次对象中途截断",
			`{"first":{"axle_loads_kg":[1`,
			"首次", true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := doCompare(t, r, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
			out := mustJSONMap(t, raw)
			errMsg, _ := out["error"].(string)
			assert.Contains(t, errMsg, tc.side)
			if tc.wantIncomplete {
				assert.Contains(t, errMsg, "不完整", "截断类错误应明确说明数据不完整: %s", errMsg)
			}
			assert.NotContains(t, string(raw), "first_result")
			assert.NotContains(t, string(raw), "retest_result")
		})
	}

	// 顶层结构在任何一份数据之前截断：无法归因到具体某一份，报整体不完整。
	code, raw := doCompare(t, r, `{"firs`)
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	out := mustJSONMap(t, raw)
	assert.Contains(t, out["error"], "JSON 不完整")
}

// 第二份非法时不得有任何部分结果：即便 first 合法可裁决，也只返回错误。
func TestComparison_SecondInvalidHasNoPartialResults(t *testing.T) {
	r := newRouter(t)
	body := `{
		"first":  {"axle_loads_kg":[20000,20000,10000],"axle_spacings_mm":[1801,1801]},
		"retest": {"axle_loads_kg":[10000,10000],"axle_spacings_mm":[1801],"scale_weight_kg":21001}
	}`
	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	out := mustJSONMap(t, raw)
	require.Contains(t, out, "error")
	assert.Contains(t, out["error"], "重测")
	assert.NotContains(t, string(raw), "first_result")
	assert.NotContains(t, string(raw), "retest_result")
	assert.NotContains(t, string(raw), "groups")
}

// 变化区间按首轴序号稳定排序：多段分组边界变化与多段超限翻转均车头向后排列。
func TestComparison_ChangesSortedByStartAxle(t *testing.T) {
	r := newRouter(t)
	// 7 轴：首次 (1,2)(3,4)(5)(6)(7)，重测全部单轴组 -> 边界变化段 [1,2]、[3,4]。
	// 超限翻转：第 1 轴 超→合规、第 4 轴 超→合规（均为 true→false，被不变化轴隔开
	// 不合并），第 6 轴 合规→超，得到三个独立区间 [1,1] [4,4] [6,6]。
	body := `{
		"first":  {"axle_loads_kg":[20000,1,10001,10000,1,1,1],"axle_spacings_mm":[1800,1801,1800,1801,1801,1801]},
		"retest": {"axle_loads_kg":[9000,10001,10001,9000,1,10001,1],"axle_spacings_mm":[1801,1801,1801,1801,1801,1801]}
	}`
	code, raw := doCompare(t, r, body)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)
	assert.Equal(t, "结论改变", out["conclusion"])
	assert.NotContains(t, out, "vehicle_conclusion_change")

	bcs := out["group_boundary_changes"].([]any)
	require.Len(t, bcs, 2)
	assert.Equal(t, float64(1), bcs[0].(map[string]any)["start_axle"])
	assert.Equal(t, float64(3), bcs[1].(map[string]any)["start_axle"])

	ocs := out["over_limit_changes"].([]any)
	require.Len(t, ocs, 3)
	assert.Equal(t, float64(1), ocs[0].(map[string]any)["start_axle"])
	assert.Equal(t, float64(4), ocs[1].(map[string]any)["start_axle"])
	assert.Equal(t, float64(6), ocs[2].(map[string]any)["start_axle"])
}

// ---------------------------------------------------------------------------
// JSON 请求契约：媒体类型与字段名严格校验
// ---------------------------------------------------------------------------

// 非 JSON 媒体类型（含缺失 Content-Type）必须按 JSON 请求契约整体 422 拒绝，
// 不得进入裁决；两个 POST 入口行为一致。
func TestNonJSONContentTypeRejected(t *testing.T) {
	r := newRouter(t)
	verifyBody := `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}`
	compareBody := `{"first":` + verifyBody + `,"retest":` + verifyBody + `}`
	for _, path := range []string{"/api/v1/verify", "/api/v1/retest-comparison"} {
		body := verifyBody
		if path == "/api/v1/retest-comparison" {
			body = compareBody
		}
		for _, ct := range []string{"text/plain", "text/html", "application/xml",
			"application/x-www-form-urlencoded", ""} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
			if ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnprocessableEntity, w.Code,
				"%s 以 Content-Type %q 提交必须 422", path, ct)
			out := mustJSONMap(t, w.Body.Bytes())
			require.Contains(t, out, "error")
			// 拒绝时绝不夹带任何裁决或比对产物。
			for _, kw := range []string{"groups", "vehicle", "violations",
				"first_result", "retest_result", "conclusion"} {
				assert.NotContains(t, w.Body.String(), kw, "422 响应不得出现 %s", kw)
			}
		}
	}
}

// application/json 携带 charset 等参数仍是合法 JSON 媒体类型，两个入口均须接受。
func TestJSONContentTypeWithParametersAccepted(t *testing.T) {
	r := newRouter(t)
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/verify",
			bytes.NewBufferString(`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`))
		req.Header.Set("Content-Type", ct)
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "Content-Type %q 应被接受", ct)
	}
}

// 字段名须与明确契约逐字符一致：全大写、混合大小写等变体一律按未知字段 422 拒绝。
func TestVerify_CaseVariantFieldsRejected(t *testing.T) {
	r := newRouter(t)
	for _, body := range []string{
		`{"AXLE_LOADS_KG":[9500,9500],"axle_spacings_mm":[1800]}`,
		`{"axle_loads_kg":[9500,9500],"AXLE_SPACINGS_MM":[1800]}`,
		`{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800],"SCALE_WEIGHT_KG":18000}`,
		`{"Axle_Loads_Kg":[9500,9500],"axle_spacings_mm":[1800]}`,
	} {
		code, out := doVerify(t, r, body)
		assert.Equal(t, http.StatusUnprocessableEntity, code, body)
		require.Contains(t, out, "error")
		assert.Contains(t, out["error"], "unknown field")
		assert.NotContains(t, out, "groups")
	}
}

// 重测比对中任一份数据的字段名大小写变体同样按未知字段拒绝，且错误须归因到对应那份。
func TestComparison_CaseVariantFieldsRejected(t *testing.T) {
	r := newRouter(t)
	valid := `{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}`
	cases := []struct {
		name      string
		body      string
		wantInErr string
	}{
		{"重测全大写载荷字段", `{"first":` + valid + `,"retest":{"AXLE_LOADS_KG":[9500,9500],"axle_spacings_mm":[1801]}}`, "重测"},
		{"首次全大写载荷字段", `{"first":{"AXLE_LOADS_KG":[9500,9500],"axle_spacings_mm":[1801]},"retest":` + valid + `}`, "首次"},
		{"重测混合大小写", `{"first":` + valid + `,"retest":{"Axle_Loads_Kg":[9500,9500],"axle_spacings_mm":[1801]}}`, "重测"},
		{"首次地磅字段全大写", `{"first":{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801],"SCALE_WEIGHT_KG":19000},"retest":` + valid + `}`, "首次"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := doCompare(t, r, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
			out := mustJSONMap(t, raw)
			require.Contains(t, out, "error")
			assert.Contains(t, out["error"], tc.wantInErr)
			assert.Contains(t, out["error"], "unknown field")
			assert.NotContains(t, string(raw), "first_result")
			assert.NotContains(t, string(raw), "retest_result")
		})
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}
