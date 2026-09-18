# 夜间货车轴组复核 API（axleverify）

纯后端 HTTP API，输入按车头到车尾排列的轴载荷与相邻轴距，自动完成**轴组划分**与**超限裁决**，
消除人工目测在 1800mm 临界轴距上的分歧：**1800mm 与 1801mm 必须得到可重复、唯一的执法结论**。
另设独立的**桥面承载窗口分析**入口：临时便桥值守人员提交轴位置与载荷、桥面有效长度与
核定载荷，即可在车辆上桥前判断任一时刻落在有效桥面的轴载合计是否超过现场核定值；
选填预计车速时，进一步给出超载区段与累计超载时长，供判断超载暴露多久。

- 语言 / 框架：Go 1.25 + Gin
- 裁决代码独立于 HTTP，全部规则以 testify 测试锁定（`internal/verify/verify_test.go`）
- 桥面承载窗口分析为独立领域包（`internal/bridge`），不调用轴组裁决与重测比对
- 无数据库、无外部依赖，单容器即可运行

---

## 1. 启动方法

### Docker Compose（推荐）

```bash
# 构建并以后台方式启动 API（宿主端口默认 8080）
docker compose up -d --build

# 健康检查
curl -s http://localhost:8080/healthz
# {"status":"ok"}
```

用 `API_PORT` 覆盖宿主端口（容器内始终监听 8080）：

```bash
API_PORT=9090 docker compose up -d --build
curl -s http://localhost:9090/healthz
```

停止：

```bash
docker compose down
```

### 一次性验收服务 `verify`

Compose 中定义了名为 **`verify`** 的一次性服务：它等待 API 就绪后执行黑盒验收
（1800/1801 临界两侧结论翻转、四轴组 422 且无部分结果、相同输入响应逐字节一致、
地磅校准后超限结论翻转、偏差超 5% 拒绝、未携带地磅重量的响应逐字节回归，以及
重测比对的两次一致确认、载荷变化超限翻转、轴距变化分组重排、第二份非法无部分结果、
非 JSON 媒体类型拒绝、字段名大小写变体按未知字段拒绝，以及桥面承载窗口分析的
边界恰好容纳前后轴计入载荷、平移后峰值触发拦停、并列峰值选择最早事件、
位置重复只返回错误信封，以及选填车速下持续超载的区段载荷与向上取整时长、
载荷变化的区段不合并、同位移瞬时超载仅影响原峰值且累计为零、
非法车速 422 无分析结果、省略车速的响应逐字节不变等），
全部通过则退出码 0。

```bash
docker compose build
docker compose run --rm verify
```

### 本地直接运行（需 Go 1.25）

```bash
go test ./...                       # 运行全部 testify 测试
go run ./cmd/server                 # 默认 :8080
API_PORT=9090 go run ./cmd/server   # 指定端口
```

---

## 2. 请求契约（先读本节）

### `POST /api/v1/verify`

- `Content-Type: application/json`（文本等其它媒体类型、或缺失该头，一律拒绝，HTTP 422）
- 请求体为单个 JSON 对象，未知字段一律拒绝（HTTP 422）；字段名须与下表**逐字符一致**，
  大小写变体（如 `AXLE_LOADS_KG`）同样视为未知字段

| 字段 | 类型 | 含义 | 约束 |
| --- | --- | --- | --- |
| `axle_loads_kg` | 整数数组 | **按车头到车尾**排列的各轴载荷，千克 | 必填；轴数 1～12；每项 1～20000 |
| `axle_spacings_mm` | 整数数组 | **相邻轴距**（第 1 项为第 1-2 轴间距，依此类推），毫米 | 必填；项数必须恰好比轴数少 1 项；每项 500～10000 |
| `scale_weight_kg` | 整数 | **选填**：收费站地磅整车重量，千克；提供时先校准各轴载荷再裁决 | 1～240000；与轴载荷合计偏差须 ≤ 5%；缺省或 `null` 时不校准 |

任一约束不满足（含字段缺失、非整数、空数组、多/少轴距项、数值越界、
地磅重量越界或偏差超 5%）都**统一返回 HTTP 422**，
响应体只有错误信息，**不会给出任何部分裁决结果**。
未携带 `scale_weight_kg` 的请求，响应与引入校准能力前逐字节一致。

### 裁决规则（已由测试锁定，非占位实现）

1. 从第 1 轴起遍历相邻轴距：
   - 轴距 **≤ 1800mm**：后轴并入当前轴组；
   - 轴距 **> 1800mm（即 1801mm 起）**：后轴开启下一个轴组。
2. 轴组限值按组内轴数取值：**单轴 10000、双轴 18000、三轴 24000 千克**。
3. 一旦形成**四轴及以上轴组**，输入非法（HTTP 422），不输出结果。
4. 组载荷与整车总载荷均为**整数求和**；载荷 **等于限值判定为合规**，仅“大于”才超限。
5. 整车限值固定为 **49000 千克**。
6. 响应中轴序号、组序号均从 **1** 开始（车头方向为 1）。
7. **地磅校准（选填）**：携带 `scale_weight_kg` 时，先计算地磅重量与轴载荷合计的差额，
   按各轴原载荷比例分摊整数千克（每轴取精确份额的向下取整部分），
   剩余整数千克按**小数部分从大到小、轴序号从小到大**逐轴补 1，
   保证校准后各轴之和严格等于地磅重量且结果可重复；
   后续轴组划分、轴组与整车裁决全部使用校准后的载荷。
8. 地磅重量与轴载荷合计的**偏差超过 5%**（恰好 5% 允许）时返回 422，只说明偏差超界。

### 成功响应（HTTP 200）

按顺序包含三部分：先列出**每个轴组**（首尾轴序号、组载荷、限值、是否超限），
再列出**整车结果**，最后是 `violations` 超限清单——组超限按组序号升序排列，
**整车超限固定置于最后**（无超限时为空数组）。

```json
{
  "groups": [
    {
      "index": 1,
      "start_axle": 1,
      "end_axle": 1,
      "axle_count": 1,
      "load_kg": 20000,
      "limit_kg": 10000,
      "over_limit": true
    }
  ],
  "vehicle": {
    "load_kg": 20000,
    "limit_kg": 49000,
    "over_limit": false
  },
  "violations": [
    { "scope": "group", "index": 1 }
  ]
}
```

整车超限时追加 `{ "scope": "vehicle" }`（无 `index` 字段）。

### 临界示例：1800 vs 1801 毫米

同样两轴各 9500kg，结论随 1mm 确定性翻转：

```bash
# 1800mm：两轴同组，双轴组 19000 > 18000，超限
curl -s -X POST localhost:8080/api/v1/verify \
  -H 'Content-Type: application/json' \
  -d '{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]}'
# groups[0]: axle_count=2 load=19000 limit=18000 over_limit=true
# violations: [{"scope":"group","index":1}]

# 1801mm：开启下一组，两个单轴组各 9500 <= 10000，全部合规
curl -s -X POST localhost:8080/api/v1/verify \
  -H 'Content-Type: application/json' \
  -d '{"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}'
# groups: 2 个单轴组，均 over_limit=false；violations: []
```

### 地磅校准示例：超限结论翻转

两轴各 9200kg、轴距 1800mm：双轴组 18400 > 18000 原本超限；
携带地磅重量 18000 后校准为 [9000, 9000]，组载荷等于限值合规：

```bash
curl -s -X POST localhost:8080/api/v1/verify \
  -H 'Content-Type: application/json' \
  -d '{"axle_loads_kg":[9200,9200],"axle_spacings_mm":[1800],"scale_weight_kg":18000}'
```

```json
{
  "groups": [
    {
      "index": 1,
      "start_axle": 1,
      "end_axle": 2,
      "axle_count": 2,
      "load_kg": 18000,
      "limit_kg": 18000,
      "over_limit": false
    }
  ],
  "vehicle": { "load_kg": 18000, "limit_kg": 49000, "over_limit": false },
  "violations": [],
  "calibration": {
    "scale_weight_kg": 18000,
    "original_total_kg": 18400,
    "difference_kg": -400,
    "calibrated_loads_kg": [9000, 9000]
  }
}
```

`calibration` 仅在携带 `scale_weight_kg` 时出现：依次给出地磅整车重量、
校准前轴载荷合计、校准差额（可正可负）与各轴校准载荷；
`groups` / `vehicle` / `violations` 均按校准后的载荷计算。

### 错误响应（HTTP 422）

```json
{ "error": "第 1 项轴距 10001 超出允许范围 500-10000 毫米" }
```

422 响应中绝不出现 `groups` / `vehicle` / `violations`。

### `GET /healthz`

返回 `200 {"status":"ok"}`，供容器探活与验收服务等待就绪使用。

### `POST /api/v1/retest-comparison`（夜间重测比对）

夜间执法完成首次称重后，复核员常会要求车辆重新停稳再测一次。本入口接收**同一车辆**
的首次与重测两份数据，分别调用既有裁决链路后返回两份**完整裁决结果**，并自动给出
两份结论是否一致，无需人工比对两份响应。

- 请求体为单个 JSON 对象，包含 `first`（首次称重）与 `retest`（重测）两个字段，
  二者各自是一个与 `/api/v1/verify` **字段契约完全相同**的对象
  （`axle_loads_kg`、`axle_spacings_mm` 必填，`scale_weight_kg` 选填，
  未知字段一律拒绝，大小写变体同样视为未知字段）。
- `Content-Type` 同样须为 `application/json`，媒体类型不符或缺失时整体 422。
- 两份数据的**轴数必须相同**。

任一份数据非法（含未知字段、缺字段、数值越界、四轴组、地磅偏差超 5% 等）
或两份轴数不一致时，**整体返回 HTTP 422**，错误信息明确指出是
**“首次称重数据”还是“重测数据”**及具体原因，且**绝不夹带另一份裁决结果**。
即使某一份数据在填写到一半时被截断（导致整个外层 JSON 不完整），错误仍会
归因到当时正在读取的那一份，例如：

```json
{ "error": "重测数据不完整：JSON 在读取该字段时被截断，须提交包含 axle_loads_kg 与 axle_spacings_mm 的完整 JSON 对象" }
```

```bash
curl -s -X POST localhost:8080/api/v1/retest-comparison \
  -H 'Content-Type: application/json' \
  -d '{
    "first":  {"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1800]},
    "retest": {"axle_loads_kg":[9500,9500],"axle_spacings_mm":[1801]}
  }'
```

成功响应（HTTP 200）依次包含：

| 字段 | 含义 |
| --- | --- |
| `first_result` / `retest_result` | 两次称重各自的**完整裁决结果**，结构与单次裁决响应逐字段一致（携带地磅重量时各自带 `calibration`） |
| `group_boundary_changes` | **分组边界发生变化**的轴覆盖区间；每项给出区间 `start_axle`/`end_axle`，以及首次与重测在该区间内的分组覆盖 `first_groups` / `retest_groups` |
| `over_limit_changes` | **超限状态发生翻转**的轴覆盖区间（按轴逐轴比对“所属轴组是否超限”后合并连续同状态轴）；给出两侧 `first_over_limit` / `retest_over_limit` |
| `vehicle_conclusion_change` | 仅在**整车超限结论翻转**时出现，携带两侧整车总重与超限标志；否则缺省 |
| `conclusion` | **`结论确认`**（无任何变化）或 **`结论改变`**（任一分组边界、区间超限状态或整车结论变化） |

`group_boundary_changes` 与 `over_limit_changes` 的所有条目均按**首轴序号稳定升序**
（车头方向）排列；无变化时为空数组 `[]`。

上例（1800mm 双轴超限组 vs 1801mm 两个合规单轴组）的响应要点：

```json
{
  "first_result":  { "groups": [ {"start_axle":1,"end_axle":2,"over_limit":true} ] },
  "retest_result": { "groups": [ {"start_axle":1,"end_axle":1,"over_limit":false},
                                 {"start_axle":2,"end_axle":2,"over_limit":false} ] },
  "group_boundary_changes": [
    { "start_axle": 1, "end_axle": 2,
      "first_groups":  [ {"start_axle":1,"end_axle":2} ],
      "retest_groups": [ {"start_axle":1,"end_axle":1}, {"start_axle":2,"end_axle":2} ] }
  ],
  "over_limit_changes": [
    { "start_axle": 1, "end_axle": 2, "first_over_limit": true, "retest_over_limit": false }
  ],
  "conclusion": "结论改变"
}
```

比对入口不改变单次裁决的任何行为：`POST /api/v1/verify` 的请求/响应字节、
校准行为、`GET /healthz` 与 `API_PORT` 覆盖均保持原样。

### `POST /api/v1/bridge-window`（桥面承载窗口分析）

临时便桥值守人员在车辆上桥前，提交按车头方向严格递增的轴位置与对应载荷、
桥面有效长度和现场核定载荷，本入口扫描车辆平移过程中的全部进入与离开事件，
返回任一时刻落在有效桥面的轴载合计最大值及通行或拦停结论。
选填预计车速时，响应在原峰值字段后追加**超载区段**与**累计超载时长**，
供值守人员结合车辆匀速通过的预计车速判断超载暴露多久。
本入口为独立模块，不调用轴组裁决与重测比对。

- `Content-Type: application/json`（其它媒体类型或缺失该头，一律 422）
- 请求体为单个 JSON 对象，未知字段一律 422；字段名须与下表**逐字符一致**，
  大小写变体同样视为未知字段

| 字段 | 类型 | 含义 | 约束 |
| --- | --- | --- | --- |
| `axle_positions_mm` | 整数数组 | 各轴位置，毫米，**按车头方向严格递增**（第 1 项为车尾轴，末项为车头轴） | 必填；轴数 1～12；每项 ≥ 0 且不重复，**不设上限** |
| `axle_loads_kg` | 整数数组 | 与轴位置一一对应的轴载荷，千克 | 必填；项数与轴位置一致；每项 ≥ 1，**不设上限** |
| `bridge_length_mm` | 整数 | 桥面有效长度，毫米 | 必填；1000～50000 |
| `approved_load_kg` | 整数 | 现场核定载荷，千克 | 必填；1～200000 |
| `speed_mm_per_s` | 整数 | **选填**：车辆匀速通过的预计车速，毫米每秒；提供时追加计算超载区段与累计时长 | 1～50000；缺省或 `null` 时不计算，响应与未提供时逐字节一致 |

任一约束不满足（含字段缺失、位置重复或未递增、数值越界、
车速类型错误/为零/越界）都**统一返回 HTTP 422**，
响应体只有错误信封 `{"error": ...}`，**不会生成任何分析结果**。
轴位置与轴载荷不设上限：落桥载荷按**任意精度整数**求和，
离开事件位移的排序对超出 int64 范围的情形做了保序处理，
极大数值不会溢出，最大桥面载荷不会被报成零或少算，
`max_load_kg` 始终作为 JSON 数字精确返回。

### 分析规则（已由测试锁定，非占位实现）

1. **位移原点**：车头轴抵达桥入口（桥面坐标 0）的时刻位移为 0，此后车辆每前进
   1 毫米位移加 1；轴 i 的桥面坐标 = 位移 −（车头轴位置 − 轴 i 位置）。
2. 轴的桥面坐标落在 **[0, 桥长] 闭区间**内即落桥：桥面边界恰好容纳的轴
   （坐标 0 或桥长处）同样计入载荷。
3. 领域对象为**连续落桥轴区间**：车辆平移过程中落在桥面的轴始终构成一段连续
   区间，每次进入或离开事件使其更新；同一位移处进入事件先于离开事件处理。
4. 扫描全部事件后取**桥面载荷最大值**；相同最大值出现多次时，依次按
   **车辆位移最小、首轴序号最小**确定唯一结果。
5. 最大桥面载荷 **≤ 核定载荷判定通行**（含相等），否则拦停。
6. 响应中轴序号从 **1** 开始，按提交顺序编号（1 为车尾轴）。
7. **超载区段（选填车速时计算）**：领域层复用同一组轴进入与离开事件，
   相邻事件位移之间载荷恒定，构成候选区段；只保留**位移长度大于零**且
   **载荷超过核定值**的区段，**相邻且载荷相同的区段先合并**，
   再按“位移差×1000÷车速”**向上取整**给出各区段毫秒数，累计为总时长。
   同一位移处连续事件之间的瞬时状态（位移长度为零）仍参与第 4 条峰值裁决，
   但**不进入区段与累计时长**。
8. 区段载荷与时长按**任意精度整数**计算与输出；位移超出 int64 范围的
   离开事件不产出区段（其真实位移无法用 int64 表示，与第 4 条不产出
   窗口快照同理）。

### 成功响应（HTTP 200）

```bash
# 三轴车，桥面有效长度恰好等于首尾轴距：平移至车头轴抵桥出口、
# 车尾轴抵桥入口的瞬间（位移 3000），边界前后轴均计入，峰值 12000。
curl -s -X POST localhost:8080/api/v1/bridge-window \
  -H 'Content-Type: application/json' \
  -d '{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":12000}'
```

```json
{
  "max_load_kg": 12000,
  "first_axle": 1,
  "last_axle": 3,
  "displacement_mm": 3000,
  "conclusion": "通行"
}
```

| 字段 | 含义 |
| --- | --- |
| `max_load_kg` | 整个平移过程中落在有效桥面的轴载合计最大值，千克 |
| `first_axle` / `last_axle` | 取得最大值时连续落桥轴区间的首尾轴序号（1 基，按提交顺序） |
| `displacement_mm` | 取得最大值时的车辆位移，毫米（车头轴抵桥入口为 0） |
| `conclusion` | **`通行`**（最大值 ≤ 核定载荷）或 **`拦停`**（最大值 > 核定载荷） |
| `overload_segments` | **仅携带 `speed_mm_per_s` 时出现**：超载区段清单，按起始位移升序；无超载区段时为空数组 `[]` |
| `total_overload_duration_ms` | **仅携带 `speed_mm_per_s` 时出现**：各区段时长的累计值，毫秒；无超载区段时为 `0` |

`overload_segments` 的每一项：

| 字段 | 含义 |
| --- | --- |
| `start_displacement_mm` / `end_displacement_mm` | 区段起止车辆位移，毫米；位移长度恒大于零 |
| `load_kg` | 该区段内的恒定载荷，千克，恒大于核定载荷 |
| `duration_ms` | 以给定车速通过该区段的时长，毫秒，按“位移差×1000÷车速”**向上取整** |

携带车速的示例：三轴车以 7 毫米每秒通过，位移 1500-4500 区间载荷恒定
8000 千克超过核定值 7000；位移 3000 处的瞬时峰值 12000 仍决定原峰值与结论，
但其位移长度为零不进入区段，两侧同载荷区段合并为一段，
时长 3000×1000÷7 向上取整为 428572 毫秒：

```bash
curl -s -X POST localhost:8080/api/v1/bridge-window \
  -H 'Content-Type: application/json' \
  -d '{"axle_positions_mm":[0,1500,3000],"axle_loads_kg":[4000,4000,4000],"bridge_length_mm":3000,"approved_load_kg":7000,"speed_mm_per_s":7}'
```

```json
{
  "max_load_kg": 12000,
  "first_axle": 1,
  "last_axle": 3,
  "displacement_mm": 3000,
  "conclusion": "拦停",
  "overload_segments": [
    { "start_displacement_mm": 1500, "end_displacement_mm": 4500, "load_kg": 8000, "duration_ms": 428572 }
  ],
  "total_overload_duration_ms": 428572
}
```

未携带 `speed_mm_per_s`（或填 `null`）的请求，响应与引入车速能力前**逐字节一致**，
绝不出现 `overload_segments` 与 `total_overload_duration_ms`。

### 错误响应（HTTP 422）

```json
{ "error": "第 2 轴与第 3 轴位置重复（均为 2000 毫米），轴位置不得重复" }
```

422 响应中绝不出现 `max_load_kg` / `first_axle` / `last_axle` / `displacement_mm` / `conclusion` /
`overload_segments` / `total_overload_duration_ms`。

---

## 3. 项目结构

```
cmd/server/main.go          HTTP 服务入口（读取 API_PORT，默认 8080）
cmd/acceptance/main.go      一次性黑盒验收程序（Compose 的 verify 服务）
internal/verify/verify.go   轴组划分与超限裁决（纯逻辑，无占位代码）
internal/verify/verify_test.go        testify 锁定全部裁决规则
internal/bridge/bridge.go             桥面承载窗口分析（独立领域包，纯逻辑）
internal/bridge/bridge_test.go        testify 锁定窗口扫描与并列裁决规则
internal/httpapi/handler.go           Gin 路由、请求解析与 422 处理
internal/httpapi/bridge.go            桥面承载窗口分析入口的 Gin 处理
internal/httpapi/handler_test.go      HTTP 契约测试（200/422/字段顺序）
internal/httpapi/bridge_test.go       桥面窗口 HTTP 契约测试
Dockerfile                  多阶段构建，distroless 运行镜像
docker-compose.yml          api 常驻服务 + verify 一次性验收服务
```

## 4. 规则要点速查

| 判定点 | 规则 |
| --- | --- |
| 同组边界 | 相邻轴距 ≤ 1800mm 同组；> 1800mm 另起一组 |
| 单/双/三轴组限值 | 10000 / 18000 / 24000 kg |
| 四轴及以上轴组 | 输入非法，422 |
| 等于限值 | 合规（`load > limit` 才超限） |
| 整车限值 | 49000 kg |
| 地磅重量范围 | 1～240000 kg，越界 422 |
| 地磅偏差上限 | 与轴载荷合计偏差 ≤ 5%（恰好 5% 允许），超过 422 且只说明偏差超界 |
| 差额分摊 | 按原载荷比例向下取整，余数按小数部分降序、轴序号升序逐轴补 1 |
| 超限清单顺序 | 组按序号升序，整车固定最后 |
| 非法输入 | 一律 422，仅 `{"error": ...}`，无部分结果 |
| 媒体类型 | 两个 POST 入口均要求 `Content-Type: application/json`，媒体类型不符或缺失一律 422 |
| 字段名 | 须与契约逐字符一致；大小写变体（如 `AXLE_LOADS_KG`）按未知字段 422 拒绝 |
| 重测比对 | `POST /api/v1/retest-comparison`：两份同契约数据、轴数须相同，各走既有裁决链路；返回两份完整结果、按首轴排序的分组边界/超限状态变化区间、整车结论翻转与最终 `结论确认`/`结论改变` |
| 重测比对非法 | 任一份非法或轴数不一致整体 422，错误指明首次或重测，不夹带另一份裁决结果 |
| 桥面窗口 | `POST /api/v1/bridge-window`：轴位置按车头方向严格递增且不重复（1～12 轴、位置 ≥ 0、载荷 ≥ 1，均不设上限），桥长 1000～50000mm，核定载荷 1～200000kg；扫描全部进入/离开事件，返回最大桥面载荷、首尾轴、发生位移与 `通行`/`拦停` |
| 桥面窗口车速 | `speed_mm_per_s` 选填，1～50000 毫米每秒；提供时追加超载区段与累计时长，缺省或 `null` 时响应逐字节不变；类型错误、为零或越界一律 422 且无分析结果 |
| 桥面窗口区段 | 相邻事件位移间的恒定载荷区段，只保留位移长度 > 0 且载荷 > 核定值者；相邻同载荷区段先合并，时长按“位移差×1000÷车速”向上取整；同位移瞬时峰值只参与峰值裁决，不进入区段与累计时长 |
| 桥面窗口精度 | 落桥载荷按任意精度整数求和，极大位置/载荷不溢出、不拒绝，`max_load_kg` 为精确 JSON 数字 |
| 桥面窗口边界 | 桥面坐标 [0, 桥长] 闭区间均落桥；边界恰好容纳的轴计入载荷；同位移处进入先于离开 |
| 桥面窗口并列 | 相同最大值按车辆位移最小、首轴序号最小确定唯一结果；最大值 ≤ 核定载荷（含相等）判定通行 |
| 桥面窗口非法 | 一律 422，仅 `{"error": ...}`，不生成任何分析结果 |
