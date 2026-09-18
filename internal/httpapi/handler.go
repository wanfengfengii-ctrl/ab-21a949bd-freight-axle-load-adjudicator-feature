// Package httpapi 提供车轴复核的 HTTP 接口与请求契约。
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"

	"axleverify/internal/verify"
)

// maxBodyBytes 限制请求体大小，防止超大数组绕过 1-12 轴的约束消耗资源。
const maxBodyBytes = 1 << 20

type verifyRequest struct {
	AxleLoadsKg    []int `json:"axle_loads_kg"`
	AxleSpacingsMm []int `json:"axle_spacings_mm"`
	// ScaleWeightKg 选填：收费站地磅整车重量。提供时先按比例校准各轴载荷再裁决；
	// 缺省或 null 时不校准，响应与引入该校准能力前逐字节一致。
	ScaleWeightKg *int `json:"scale_weight_kg"`
}

// compareOuterFirstKey / compareOuterRetestKey 为重测比对顶层仅有的两个键。
const (
	compareOuterFirstKey  = "first"
	compareOuterRetestKey = "retest"
)

type errorResponse struct {
	Error string `json:"error"`
}

// successResponse 成功裁决响应：先列各组，再列整车，最后列超限清单；
// 仅携带地磅重量的请求在末尾追加 calibration 校准信息。
type successResponse struct {
	Groups      []verify.GroupResult      `json:"groups"`
	Vehicle     verify.VehicleResult      `json:"vehicle"`
	Violations  []verify.Violation        `json:"violations"`
	Calibration *verify.CalibrationResult `json:"calibration,omitempty"`
}

// Register 在给定引擎上注册全部路由。
func Register(r *gin.Engine) {
	r.GET("/healthz", healthz)
	r.POST("/api/v1/verify", handleVerify)
	r.POST("/api/v1/retest-comparison", handleRetestComparison)
	r.POST("/api/v1/bridge-window", handleBridgeWindow)
}

func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleVerify(c *gin.Context) {
	// 媒体类型须符合 JSON 请求契约：文本等其它类型（或缺失）直接 422，不进入裁决。
	if !requireJSONContentType(c) {
		return
	}
	// 输入非法时统一走 422，且在产出任何裁决结果之前拒绝，杜绝部分结果。
	req, ok := decodeVerifyHandlerBody(c)
	if !ok {
		return
	}

	result, err := evaluateRequest(req)
	if err != nil {
		respond422(c, err.Error())
		return
	}

	c.JSON(http.StatusOK, successResponse{
		Groups:      result.Groups,
		Vehicle:     result.Vehicle,
		Violations:  result.Violations,
		Calibration: result.Calibration,
	})
}

func handleRetestComparison(c *gin.Context) {
	// 媒体类型须符合 JSON 请求契约：文本等其它类型（或缺失）直接 422，不进入比对。
	if !requireJSONContentType(c) {
		return
	}
	// 顶层用 token 方式逐层扫描（而非一次性解到结构体）：当某一份数据的值在
	// 解码处失败时（典型如重测数据填写到一半被截断），可把错误精确归因到
	// first 或 retest，而不是笼统地报整个请求体不完整。
	firstRaw, retestRaw, ok := parseCompareOuter(c)
	if !ok {
		return
	}

	firstReq, ok := decodeMeasurement(c, firstRaw, "首次称重数据")
	if !ok {
		return
	}
	retestReq, ok := decodeMeasurement(c, retestRaw, "重测数据")
	if !ok {
		return
	}

	comparison, err := verify.CompareRetest(
		verify.RetestMeasurement{
			AxleLoadsKg:    firstReq.AxleLoadsKg,
			AxleSpacingsMm: firstReq.AxleSpacingsMm,
			ScaleWeightKg:  firstReq.ScaleWeightKg,
		},
		verify.RetestMeasurement{
			AxleLoadsKg:    retestReq.AxleLoadsKg,
			AxleSpacingsMm: retestReq.AxleSpacingsMm,
			ScaleWeightKg:  retestReq.ScaleWeightKg,
		},
	)
	if err != nil {
		respond422(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, comparison)
}

// parseCompareOuter 用 token 逐层扫描重测比对请求的顶层 JSON 对象，
// 读取 first/retest 两个键各自对应的完整值。顶层必须恰好是单个对象，
// 且只允许 first、retest 两个键、各出现一次；任一约束不满足都整体 422。
// 关键效果：当某个键的值本身无法读完时（典型如重测数据填写到一半被截断，
// 或值内部存在语法错误），错误能归因到 first 或 retest 对应的那一份数据，
// 而不是笼统地报整个请求体不完整。
func parseCompareOuter(c *gin.Context) (firstRaw, retestRaw []byte, ok bool) {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))

	open, err := dec.Token()
	if err != nil {
		respond422(c, describeOuterDecodeError("", err))
		return nil, nil, false
	}
	delim, isDelim := open.(json.Delim)
	if !isDelim || delim != '{' {
		respond422(c, "请求体必须为包含 first 与 retest 两份测量数据的 JSON 对象")
		return nil, nil, false
	}

	values := make(map[string][]byte, 2)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			respond422(c, describeOuterDecodeError("", err))
			return nil, nil, false
		}
		key, isString := keyToken.(string)
		if !isString {
			respond422(c, "JSON 解析失败：顶层键必须为字符串")
			return nil, nil, false
		}
		if key != compareOuterFirstKey && key != compareOuterRetestKey {
			respond422(c, "JSON 解析失败：json: unknown field \""+key+"\"")
			return nil, nil, false
		}
		if _, exists := values[key]; exists {
			respond422(c, outerLabel(key)+"重复出现，first 与 retest 各只能出现一次")
			return nil, nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			// 值截断、值内部语法错误等：发生在读哪一份数据上，就归因到哪一份。
			respond422(c, describeOuterDecodeError(outerLabel(key), err))
			return nil, nil, false
		}
		values[key] = raw
	}

	// 读取闭合花括号，捕获对象未闭合即被截断的情形。
	closeTok, err := dec.Token()
	if err != nil {
		respond422(c, describeOuterDecodeError("", err))
		return nil, nil, false
	}
	if closeDelim, isDelim := closeTok.(json.Delim); !isDelim || closeDelim != '}' {
		respond422(c, "JSON 解析失败：顶层对象缺少闭合花括号")
		return nil, nil, false
	}

	// 顶层必须恰好只有一个 JSON 值，再解码一次时合法请求只能得到 io.EOF。
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			respond422(c, "请求体中存在多个 JSON 值，只允许一个 JSON 对象")
			return nil, nil, false
		}
		respond422(c, describeOuterDecodeError("", err))
		return nil, nil, false
	}

	if _, exists := values[compareOuterFirstKey]; !exists {
		respond422(c, "缺少必填字段 first（首次称重数据，字段契约与单次裁决相同）")
		return nil, nil, false
	}
	if _, exists := values[compareOuterRetestKey]; !exists {
		respond422(c, "缺少必填字段 retest（重测数据，字段契约与单次裁决相同）")
		return nil, nil, false
	}
	return values[compareOuterFirstKey], values[compareOuterRetestKey], true
}

// outerLabel 返回顶层键对应的中文数据名称，用于错误归因。
func outerLabel(key string) string {
	if key == compareOuterRetestKey {
		return "重测数据"
	}
	return "首次称重数据"
}

// decodeVerifyHandlerBody 读取并解析单次裁决请求：请求体大小受限、
// 未知字段拒绝、顶层必须恰好只有一个 JSON 值。
func decodeVerifyHandlerBody(c *gin.Context) (*verifyRequest, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		respond422(c, describeDecodeError(err))
		return nil, false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			respond422(c, "请求体中存在多个 JSON 值，只允许一个 JSON 对象")
			return nil, false
		}
		respond422(c, describeDecodeError(err))
		return nil, false
	}
	return decodeMeasurement(c, raw, "")
}

// decodeMeasurement 解析一份测量数据（轴载荷、轴距及可选地磅重量）。
// label 非空时（重测比对入口）所有错误描述都冠以“首次称重数据/重测数据”，
// 明确指出非法的是哪一份；label 为空时行为与单次裁决入口完全一致。
// data 来自 json.RawMessage，本身必定恰好是一个完整 JSON 值，无需再防多文档。
func decodeMeasurement(c *gin.Context, data []byte, label string) (*verifyRequest, bool) {
	// 比对入口中每一份测量数据都必须是 JSON 对象（拒绝数字、字符串、数组乃至 null）；
	// 单次裁决入口（label 为空）保持原有解码错误文案，故不做此前置检查。
	if label != "" {
		if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
			respond422(c, label+"必须为 JSON 对象（字段契约与单次裁决相同）")
			return nil, false
		}
	}
	// 字段名须与明确契约逐字符一致：encoding/json 的字段匹配不区分大小写
	// （AXLE_LOADS_KG 也会命中 axle_loads_kg），仅靠 DisallowUnknownFields 无法
	// 拒绝大小写变体，故先按契约名单精确扫描顶层键，变体一律按未知字段拒绝。
	if key := firstNonContractKey(data, keyAxleLoadsKg, keyAxleSpacingsMm, keyScaleWeightKg); key != "" {
		respond422(c, describeMeasurementDecodeError(label,
			errors.New("json: unknown field "+strconv.Quote(key))))
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var req verifyRequest
	if err := dec.Decode(&req); err != nil {
		respond422(c, describeMeasurementDecodeError(label, err))
		return nil, false
	}
	if req.AxleLoadsKg == nil {
		respond422(c, labelMsg(label, "缺少必填字段 axle_loads_kg（按车头到车尾排列的轴载荷，千克）"))
		return nil, false
	}
	if req.AxleSpacingsMm == nil {
		respond422(c, labelMsg(label, "缺少必填字段 axle_spacings_mm（相邻轴距，毫米，项数须比轴数少一项）"))
		return nil, false
	}
	return &req, true
}

// labelMsg 为嵌套测量数据的错误描述加上“首次/重测”前缀；
// 单次裁决入口（label 为空）保持原有错误文案逐字节不变。
func labelMsg(label, msg string) string {
	if label == "" {
		return msg
	}
	return label + msg
}

// jsonMediaType 为请求契约要求的媒体类型；charset 等参数允许携带。
const jsonMediaType = "application/json"

// requireJSONContentType 按 JSON 请求契约校验 Content-Type：媒体类型必须为
// application/json（媒体类型本身大小写不敏感，charset 等参数不影响判定）。
// 文本等其它媒体类型、或缺失 Content-Type 时整体 422，不进入任何裁决。
func requireJSONContentType(c *gin.Context) bool {
	ct := c.GetHeader("Content-Type")
	if ct == "" {
		respond422(c, "缺少 Content-Type 请求头：请求契约要求以 JSON 提交（Content-Type: application/json）")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != jsonMediaType {
		respond422(c, "请求媒体类型须为 application/json（JSON 请求契约），实际为 "+strconv.Quote(ct))
		return false
	}
	return true
}

// 测量数据明确约定的字段名；其它任何拼写（含大小写变体）都视为未知字段。
const (
	keyAxleLoadsKg    = "axle_loads_kg"
	keyAxleSpacingsMm = "axle_spacings_mm"
	keyScaleWeightKg  = "scale_weight_kg"
)

// firstNonContractKey 扫描顶层对象的键，返回第一个不在 allowed 契约名单中
// 的键名；全部合法时返回空串。data 不是合法 JSON 对象（语法损坏、非对象等）
// 时也返回空串：那些情形交由后续结构体解码按既有路径报错，此处只负责
// 拦下“语法合法但字段名不符契约”的请求。
func firstNonContractKey(data []byte, allowed ...string) string {
	dec := json.NewDecoder(bytes.NewReader(data))
	open, err := dec.Token()
	if err != nil {
		return ""
	}
	if delim, isDelim := open.(json.Delim); !isDelim || delim != '{' {
		return ""
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return ""
		}
		key, _ := keyToken.(string)
		if !slices.Contains(allowed, key) {
			return key
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return ""
		}
	}
	return ""
}

// evaluateRequest 按单次裁决契约执行裁决：带地磅重量时先校准再裁决，否则直接裁决。
func evaluateRequest(req *verifyRequest) (*verify.Result, error) {
	if req.ScaleWeightKg != nil {
		return verify.EvaluateWithScale(req.AxleLoadsKg, req.AxleSpacingsMm, *req.ScaleWeightKg)
	}
	return verify.Evaluate(req.AxleLoadsKg, req.AxleSpacingsMm)
}

func respond422(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnprocessableEntity, errorResponse{Error: msg})
}

func describeDecodeError(err error) string {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "请求体为空或 JSON 不完整，须提交包含 axle_loads_kg 与 axle_spacings_mm 的 JSON 对象"
	case errors.As(err, new(*json.UnmarshalTypeError)):
		return "字段类型错误：axle_loads_kg 与 axle_spacings_mm 必须为整数数组，scale_weight_kg 必须为整数"
	default:
		// 含语法错误、未知字段、数字写入整型失败（如 1.5、超大数）等。
		return "JSON 解析失败：" + err.Error()
	}
}

// describeOuterDecodeError 描述重测比对请求顶层扫描时的解码错误。
// label 非空（首次称重数据/重测数据）表示失败发生在读取该键的值时
// （典型为重测数据填写到一半被截断），错误须明确指出是哪一份数据不完整；
// label 为空表示失败发生在外层结构本身，无法归因到具体某一份。
func describeOuterDecodeError(label string, err error) string {
	incomplete := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	if label != "" {
		if incomplete {
			return label + "不完整：JSON 在读取该字段时被截断，" +
				"须提交包含 axle_loads_kg 与 axle_spacings_mm 的完整 JSON 对象"
		}
		return label + "的 JSON 解析失败：" + err.Error()
	}
	if incomplete {
		return "请求体为空或 JSON 不完整，须提交包含 first 与 retest 两份测量数据的 JSON 对象"
	}
	// 含语法错误等。
	return "JSON 解析失败：" + err.Error()
}

// describeMeasurementDecodeError 与 describeDecodeError 同义，但为重测比对中的
// 某一份数据冠以“首次称重数据/重测数据”，明确指出非法来源。
func describeMeasurementDecodeError(label string, err error) string {
	if label == "" {
		return describeDecodeError(err)
	}
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return label + "为空或 JSON 不完整，须提交包含 axle_loads_kg 与 axle_spacings_mm 的 JSON 对象"
	case errors.As(err, new(*json.UnmarshalTypeError)):
		return label + "的字段类型错误：axle_loads_kg 与 axle_spacings_mm 必须为整数数组，scale_weight_kg 必须为整数"
	default:
		return label + "的 JSON 解析失败：" + err.Error()
	}
}
