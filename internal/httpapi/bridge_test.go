package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doBridgeWindow(t *testing.T, r *gin.Engine, body string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bridge-window", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

// 三轴车、桥面有效长度恰好等于首尾轴距：边界上的前后轴计入载荷，
// 峰值 12000 等于核定载荷，通行。响应字段顺序与字节一并锁定。
func TestBridgeWindow_BoundaryExactFitCountsEdgeAxles(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],`+
			`"bridge_length_mm":3000,"approved_load_kg":12000}`)
	require.Equal(t, http.StatusOK, code, string(raw))
	assert.Equal(t,
		`{"max_load_kg":12000,"first_axle":1,"last_axle":3,"displacement_mm":3000,"conclusion":"通行"}`,
		string(raw))
}

// 平移后出现的峰值超过核定载荷：拦停。
func TestBridgeWindow_PeakAfterTranslationStops(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],`+
			`"bridge_length_mm":3000,"approved_load_kg":11999}`)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)
	assert.Equal(t, float64(12000), out["max_load_kg"])
	assert.Equal(t, float64(3000), out["displacement_mm"])
	assert.Equal(t, "拦停", out["conclusion"])
}

// 并列峰值：位移 2000 与 4000 同为 8000，选择最早事件。
func TestBridgeWindow_TiedPeaksChooseEarliest(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0,2000,4000],"axle_loads_kg":[5000,3000,5000],`+
			`"bridge_length_mm":2000,"approved_load_kg":8000}`)
	require.Equal(t, http.StatusOK, code, string(raw))
	out := mustJSONMap(t, raw)
	assert.Equal(t, float64(8000), out["max_load_kg"])
	assert.Equal(t, float64(2000), out["displacement_mm"])
	assert.Equal(t, float64(2), out["first_axle"])
	assert.Equal(t, float64(3), out["last_axle"])
	assert.Equal(t, "通行", out["conclusion"])
}

// 同一请求两次提交，响应逐字节一致（唯一确定结论）。
func TestBridgeWindow_Repeatable(t *testing.T) {
	r := newRouter(t)
	body := `{"axle_positions_mm":[0,2000,4000],"axle_loads_kg":[5000,3000,5000],` +
		`"bridge_length_mm":2000,"approved_load_kg":8000}`
	_, first := doBridgeWindow(t, r, body)
	_, second := doBridgeWindow(t, r, body)
	assert.Equal(t, string(first), string(second))
}

func TestBridgeWindow_422Cases(t *testing.T) {
	r := newRouter(t)
	cases := []struct {
		name string
		body string
	}{
		{"空体", ``},
		{"语法错误", `{"axle_positions_mm":[0,1500,3000],`},
		{"缺位置字段", `{"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"缺载荷字段", `{"axle_positions_mm":[0,1500,3000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"缺桥长字段", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"approved_load_kg":12000}`},
		{"缺核定载荷字段", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000}`},
		{"位置为 null", `{"axle_positions_mm":null,"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"桥长为 null", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":null,"approved_load_kg":12000}`},
		{"未知字段", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000,"extra":1}`},
		{"字段名大小写变体", `{"AXLE_POSITIONS_MM":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"位置重复", `{"axle_positions_mm":[0,2000,2000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"位置未递增", `{"axle_positions_mm":[0,3000,1500],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"位置为负", `{"axle_positions_mm":[-1,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"载荷为零", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,0,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"载荷超int64", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[9223372036854775808,1,1],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"位置超int64", `{"axle_positions_mm":[0,1500,9223372036854775808],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"桥长低于下限", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":999,"approved_load_kg":12000}`},
		{"桥长高于上限", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":50001,"approved_load_kg":12000}`},
		{"核定载荷为零", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":0}`},
		{"核定载荷高于上限", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":200001}`},
		{"轴数为零", `{"axle_positions_mm":[],"axle_loads_kg":[],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"轴数十三", `{"axle_positions_mm":[0,1,2,3,4,5,6,7,8,9,10,11,12],"axle_loads_kg":[1,1,1,1,1,1,1,1,1,1,1,1,1],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"位置项数与轴数不一致", `{"axle_positions_mm":[0,1500],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"元素非整数", `{"axle_positions_mm":[0,1500.5,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"元素为字符串", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":["4000",4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}`},
		{"桥长为小数", `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000.5,"approved_load_kg":12000}`},
		{"两个 JSON 文档", `{"axle_positions_mm":[0],"axle_loads_kg":[1],"bridge_length_mm":3000,"approved_load_kg":12000}{}`},
		{"合法对象后多余右括号", `{"axle_positions_mm":[0],"axle_loads_kg":[1],"bridge_length_mm":3000,"approved_load_kg":12000}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := doBridgeWindow(t, r, tc.body)
			assert.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
			out := mustJSONMap(t, raw)
			require.Contains(t, out, "error")
			// 统一 422：只返回错误信封，绝不夹带任何分析结果。
			for _, kw := range []string{"max_load_kg", "first_axle", "last_axle",
				"displacement_mm", "conclusion"} {
				assert.NotContains(t, string(raw), kw, "422 响应不得出现 %s", kw)
			}
		})
	}
}

// 位置重复时错误信息须明确指出重复，且响应只有错误信封。
func TestBridgeWindow_DuplicatePositionsErrorEnvelopeOnly(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0,2000,2000],"axle_loads_kg":[1,1,1],`+
			`"bridge_length_mm":3000,"approved_load_kg":10000}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	out := mustJSONMap(t, raw)
	require.Contains(t, out, "error")
	assert.Contains(t, out["error"], "重复")
	assert.Len(t, out, 1, "422 响应只能包含 error 字段")
}

// 非 JSON 媒体类型（含缺失 Content-Type）整体 422 拒绝，不进入分析。
func TestBridgeWindow_NonJSONContentTypeRejected(t *testing.T) {
	r := newRouter(t)
	body := `{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],` +
		`"bridge_length_mm":3000,"approved_load_kg":12000}`
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bridge-window", bytes.NewBufferString(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "Content-Type %q 必须 422", ct)
		out := mustJSONMap(t, w.Body.Bytes())
		require.Contains(t, out, "error")
		assert.NotContains(t, w.Body.String(), "max_load_kg")
	}
}

// application/json 携带 charset 等参数仍是合法 JSON 媒体类型。
func TestBridgeWindow_JSONContentTypeWithParametersAccepted(t *testing.T) {
	r := newRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bridge-window",
		bytes.NewBufferString(`{"axle_positions_mm":[0],"axle_loads_kg":[1],`+
			`"bridge_length_mm":1000,"approved_load_kg":1}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// 未知字段错误文案与单次裁决入口一致：json: unknown field。
func TestBridgeWindow_UnknownFieldMessage(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0],"axle_loads_kg":[1],"bridge_length_mm":1000,"approved_load_kg":1,"x":1}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	out := mustJSONMap(t, raw)
	assert.Contains(t, out["error"], "unknown field")
}

// 极大轴位置与载荷不设上限、不得溢出：三轴各 math.MaxInt64 时峰值
// 3×9223372036854775807 = 27670116110564327421，须作为 JSON 数字精确返回，
// 不得报成零或少算；响应字节一并锁定。
func TestBridgeWindow_ExtremeLoadsExactJSONNumber(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0,1500,3000],`+
			`"axle_loads_kg":[9223372036854775807,9223372036854775807,9223372036854775807],`+
			`"bridge_length_mm":3000,"approved_load_kg":200000}`)
	require.Equal(t, http.StatusOK, code, string(raw))
	assert.Equal(t,
		`{"max_load_kg":27670116110564327421,"first_axle":1,"last_axle":3,"displacement_mm":3000,"conclusion":"拦停"}`,
		string(raw))
}

// 极大轴位置：后轴离开位移（进入位移+桥长）超出 int64 仍精确计算，
// 两轴不会同时落桥，峰值为车头轴单轴载荷。
func TestBridgeWindow_ExtremePositionsExact(t *testing.T) {
	r := newRouter(t)
	code, raw := doBridgeWindow(t, r,
		`{"axle_positions_mm":[0,9223372036854775807],"axle_loads_kg":[4000,5000],`+
			`"bridge_length_mm":3000,"approved_load_kg":12000}`)
	require.Equal(t, http.StatusOK, code, string(raw))
	assert.Equal(t,
		`{"max_load_kg":5000,"first_axle":2,"last_axle":2,"displacement_mm":0,"conclusion":"通行"}`,
		string(raw))
}
