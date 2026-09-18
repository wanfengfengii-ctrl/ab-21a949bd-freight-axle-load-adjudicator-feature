package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"axleverify/internal/bridge"
)

// bridgeRequest 为桥面承载窗口分析请求：轴位置按车头方向严格递增，
// 载荷与轴一一对应，桥长与核定载荷为现场核定值。前四个字段均必填。
// SpeedMmPerS 选填：车辆匀速通过的预计车速（毫米每秒）。提供时响应在
// 原峰值字段后追加超载区段与累计时长；缺省或 null 时不计算，
// 响应与引入该能力前逐字节一致。
type bridgeRequest struct {
	AxlePositionsMm []int `json:"axle_positions_mm"`
	AxleLoadsKg     []int `json:"axle_loads_kg"`
	BridgeLengthMm  *int  `json:"bridge_length_mm"`
	ApprovedLoadKg  *int  `json:"approved_load_kg"`
	SpeedMmPerS     *int  `json:"speed_mm_per_s"`
}

// bridgeResponse 为桥面承载窗口分析成功响应：最大桥面载荷、对应首尾轴
// 序号、发生位移与通行或拦停结论。最大桥面载荷按任意精度整数输出
// （json.Number 序列化为 JSON 数字，极大载荷不会溢出成零或变小）。
type bridgeResponse struct {
	MaxLoadKg      json.Number `json:"max_load_kg"`
	FirstAxle      int         `json:"first_axle"`
	LastAxle       int         `json:"last_axle"`
	DisplacementMm int         `json:"displacement_mm"`
	Conclusion     string      `json:"conclusion"`
}

// overloadSegmentJSON 为一段载荷恒定且超过核定载荷的位移区段：
// 起止位移、区段内恒定载荷与按“位移差×1000÷车速”向上取整的毫秒数。
// 载荷与时长按任意精度整数输出（json.Number，极大数值不溢出）。
type overloadSegmentJSON struct {
	StartDisplacementMm int         `json:"start_displacement_mm"`
	EndDisplacementMm   int         `json:"end_displacement_mm"`
	LoadKg              json.Number `json:"load_kg"`
	DurationMs          json.Number `json:"duration_ms"`
}

// bridgeSpeedResponse 为携带车速时的桥面承载窗口分析成功响应：
// 原峰值字段之后依次追加超载区段清单与累计超载时长（毫秒）。
// 无超载区段时 overload_segments 为空数组、累计时长为 0。
type bridgeSpeedResponse struct {
	bridgeResponse
	OverloadSegments        []overloadSegmentJSON `json:"overload_segments"`
	TotalOverloadDurationMs json.Number           `json:"total_overload_duration_ms"`
}

// 桥面承载窗口分析明确约定的字段名；其它任何拼写（含大小写变体）都视为未知字段。
const (
	keyAxlePositionsMm = "axle_positions_mm"
	keyBridgeLengthMm  = "bridge_length_mm"
	keyApprovedLoadKg  = "approved_load_kg"
	keySpeedMmPerS     = "speed_mm_per_s"
)

func handleBridgeWindow(c *gin.Context) {
	// 媒体类型须符合 JSON 请求契约：文本等其它类型（或缺失）直接 422，不进入分析。
	if !requireJSONContentType(c) {
		return
	}
	// 输入非法时统一走 422，且在产出任何分析结果之前拒绝，杜绝部分结果。
	req, ok := decodeBridgeBody(c)
	if !ok {
		return
	}

	analysis, err := bridge.Analyze(bridge.Input{
		AxlePositionsMm: req.AxlePositionsMm,
		AxleLoadsKg:     req.AxleLoadsKg,
		BridgeLengthMm:  *req.BridgeLengthMm,
		ApprovedLoadKg:  *req.ApprovedLoadKg,
		SpeedMmPerS:     req.SpeedMmPerS,
	})
	if err != nil {
		respond422(c, err.Error())
		return
	}

	peak := bridgeResponse{
		MaxLoadKg:      json.Number(analysis.MaxLoadKg.String()),
		FirstAxle:      analysis.FirstAxle,
		LastAxle:       analysis.LastAxle,
		DisplacementMm: analysis.DisplacementMm,
		Conclusion:     analysis.Conclusion,
	}
	// 未提供车速（含显式 null）时响应只含原峰值字段，与引入车速能力前逐字节一致。
	if analysis.OverloadDurationMs == nil {
		c.JSON(http.StatusOK, peak)
		return
	}
	segments := make([]overloadSegmentJSON, len(analysis.OverloadSegments))
	for i, seg := range analysis.OverloadSegments {
		segments[i] = overloadSegmentJSON{
			StartDisplacementMm: seg.StartDisplacementMm,
			EndDisplacementMm:   seg.EndDisplacementMm,
			LoadKg:              json.Number(seg.LoadKg.String()),
			DurationMs:          json.Number(seg.DurationMs.String()),
		}
	}
	c.JSON(http.StatusOK, bridgeSpeedResponse{
		bridgeResponse:          peak,
		OverloadSegments:        segments,
		TotalOverloadDurationMs: json.Number(analysis.OverloadDurationMs.String()),
	})
}

// decodeBridgeBody 读取并解析桥面承载窗口分析请求：请求体大小受限、
// 未知字段（含大小写变体）拒绝、顶层必须恰好只有一个 JSON 值。
func decodeBridgeBody(c *gin.Context) (*bridgeRequest, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		respond422(c, describeBridgeDecodeError(err))
		return nil, false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			respond422(c, "请求体中存在多个 JSON 值，只允许一个 JSON 对象")
			return nil, false
		}
		respond422(c, describeBridgeDecodeError(err))
		return nil, false
	}
	// 字段名须与明确契约逐字符一致：encoding/json 的字段匹配不区分大小写，
	// 仅靠 DisallowUnknownFields 无法拒绝大小写变体，故先按契约名单精确扫描。
	if key := firstNonContractKey(raw,
		keyAxlePositionsMm, keyAxleLoadsKg, keyBridgeLengthMm, keyApprovedLoadKg, keySpeedMmPerS); key != "" {
		respond422(c, describeBridgeDecodeError(
			errors.New("json: unknown field "+strconv.Quote(key))))
		return nil, false
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	var req bridgeRequest
	if err := strict.Decode(&req); err != nil {
		respond422(c, describeBridgeDecodeError(err))
		return nil, false
	}
	if req.AxlePositionsMm == nil {
		respond422(c, "缺少必填字段 axle_positions_mm（按车头方向严格递增的轴位置，毫米）")
		return nil, false
	}
	if req.AxleLoadsKg == nil {
		respond422(c, "缺少必填字段 axle_loads_kg（与轴位置一一对应的轴载荷，千克）")
		return nil, false
	}
	if req.BridgeLengthMm == nil {
		respond422(c, "缺少必填字段 bridge_length_mm（桥面有效长度，毫米）")
		return nil, false
	}
	if req.ApprovedLoadKg == nil {
		respond422(c, "缺少必填字段 approved_load_kg（现场核定载荷，千克）")
		return nil, false
	}
	return &req, true
}

// describeBridgeDecodeError 描述桥面承载窗口分析请求的解码错误，
// 文案结构与单次裁决入口一致，仅字段清单换成本入口契约。
func describeBridgeDecodeError(err error) string {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "请求体为空或 JSON 不完整，须提交包含 axle_positions_mm、axle_loads_kg、" +
			"bridge_length_mm 与 approved_load_kg 的 JSON 对象"
	case errors.As(err, new(*json.UnmarshalTypeError)):
		return "字段类型错误：axle_positions_mm 与 axle_loads_kg 必须为整数数组，" +
			"bridge_length_mm、approved_load_kg 与 speed_mm_per_s 必须为整数"
	default:
		// 含语法错误、未知字段、数字写入整型失败（如 1.5、超大数）等。
		return "JSON 解析失败：" + err.Error()
	}
}
