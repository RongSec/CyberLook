package service

import (
	"errors"
	"strings"
	"sync"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ===== AI Agent 提示词 =====
//
// 两个角色，各一份提示词，都可以在「LLM 配置」页编辑：
//
//	judge    ── 研判：这一页是否异常（错误页/壳页/跳转页/粒度不足），要不要渲染
//	identify ── 识别：从证据里给出业务系统名
//
// 为什么拆开：这两件事的判断依据不同。研判要的是"页面长什么样"（状态码、正文量、
// 跳转链），识别要的是"页面里写了什么"（标题、特有路径、版权行）。写在同一个
// 提示词里时，模型经常为了给名字而忽略"这其实是错误页"，或者反过来为了说
// "证据不足"而不肯输出已经看得到的产品名。
//
// 提示词本身刻意写得短、条目化：长提示词里的解释性段落会被模型当成"可选项"，
// 反而稀释真正的硬规则。

const (
	// RoleJudge 研判 agent
	RoleJudge = "judge"
	// RoleIdentify 识别 agent
	RoleIdentify = "identify"
)

// DefaultJudgePrompt 研判 agent 的默认提示词
const DefaultJudgePrompt = `你是内网资产的页面研判器。读 HTTP 证据，判断这一页是否异常、要不要用浏览器渲染。

只输出一个 JSON，不要解释、不要 markdown：
{"kind":"normal","render":false,"reason":"15字以内"}

kind 取值：
- normal：2xx/3xx，标题明确，正文有实际内容
- error：4xx/5xx，或正文只有状态文本（502 Bad Gateway、403 Forbidden、Not Found、Apache/nginx 默认页）
- shell：正文几乎为空、只有脚本外壳，或标题为 Login、Welcome、Index of /、空
- jump：多跳跳转、纯跳转页（meta refresh、location.replace、Redirection……）
- weak：页面正常，但只有厂商级线索，缺产品线名（只看到 Sangfor，没写 HCI 还是 aTrust）

render 规则：
- normal、error → false，浏览器打开还是同一段文字
- shell、jump、weak → true
- 无响应、非 HTML → kind 给 error，render 给 false`

// DefaultIdentifyPrompt 识别 agent 的默认提示词
const DefaultIdentifyPrompt = `你是内网资产识别专家。读 HTTP 证据，给出上面跑的是什么业务系统，并判断这份证据够不够。

只输出一个 JSON，不要解释、不要 markdown：
{"title":"业务系统名称","product":"产品/组件（含厂商）","category":"OA/财务/审计/中间件","confidence":0,"need_render":false,"reason":"15字以内"}

规则：
1. 只用证据里的信息，不要编造。
2. title 与 product 至少填一个，禁止两个都空。判断不出具体系统时 title 留空，
   product 必须写技术栈或厂商（IIS、nginx、Tomcat、Spring、Kubernetes、SAP NetWeaver）。
3. title 写人能看懂的系统名，不写 nginx、Apache、IIS 这类服务器名（那些放 product）。
4. 多跳时只看最后一跳，中间跳转页的文字一律不用。
5. 禁止作为 title：Redirection、307 TEMPORARY REDIRECT、Login、Welcome、Index of /、Error 404、纯数字、纯符号。
6. confidence：90+ 页面直接写出产品名；60-89 有明确特征（特有路径、证书、界面元素）；30-59 只到类别；<30 是靠猜。
7. 区分厂商和产品线。只有 Sangfor、H3C、Huawei 这类厂商特征、没有产品线文字时，confidence 不超过 55。
8. 页面是错误页或空壳页时，title 留空，但 product 仍要给出可见的技术栈；reason 写页面类型。
9. need_render：把"证据不足"和"这就是结论"分开，不要因为页面像错误页就直接给 false。
   - true：正文几乎为空；整页只有 <script> 外壳；或 HTTP 200 但标题是占位词
     （Login / Welcome / Error / Browser Error / 重定向中 / 纯 IP）。这类页面真实身份
     常由 JS 在渲染后写进 title，不渲染就永远看不到。
   - false：证据已足够定论；或页面就是接口响应 / 纯错误页（4xx-5xx 且正文只有状态文本、
     JSON、纯文本、空 body）。
10. 证据开头可能有一行「引擎指纹」：规则库对同一份响应的静态匹配结果，只作线索。
    与页面内容冲突时以页面内容为准（规则库会把空标题的 Angular 壳误判成 spring_cloud_config）。
    不要把规则命中当成最终答案照抄，也不要在输出里提到它。
11. 输出必须是一个完整的 JSON 对象，不要写分析、不要复述证据。`

// promptLock 串行化提示词写操作（读是单行主键查询，不需要锁）
var promptLock sync.Mutex

// DefaultPrompt 返回某角色的内置提示词；未知角色返回空串。
func DefaultPrompt(role string) string {
	switch role {
	case RoleJudge:
		return DefaultJudgePrompt
	case RoleIdentify:
		return DefaultIdentifyPrompt
	}
	return ""
}

// PromptFor 返回实际生效的提示词：库里有自定义就用自定义，否则用默认。
//
// 每次调用都读一次库（ai_settings 只有一行，走主键查询，微秒级）。
// 不缓存是有意的：用户在配置页点保存后，下一条识别就要用新提示词，
// 若缓存还得做失效通知，收益远小于复杂度。
func PromptFor(role string) string {
	var s model.AISettings
	custom := ""
	if err := config.DB.First(&s, 1).Error; err == nil {
		switch role {
		case RoleJudge:
			custom = s.JudgePrompt
		case RoleIdentify:
			custom = s.IdentifyPrompt
		}
	}
	if t := strings.TrimSpace(custom); t != "" {
		return t
	}
	return DefaultPrompt(role)
}

// PromptInfo 一个角色的提示词展示信息
type PromptInfo struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	Default   string `json:"default"`
	IsDefault bool   `json:"isDefault"`
}

// AllPrompts 返回两个角色的提示词（含默认值，前端用来做"恢复默认"和差异提示）
func AllPrompts() []PromptInfo {
	out := make([]PromptInfo, 0, 2)
	for _, role := range []string{RoleJudge, RoleIdentify} {
		text := PromptFor(role)
		def := DefaultPrompt(role)
		out = append(out, PromptInfo{
			Role: role, Text: text, Default: def,
			IsDefault: strings.TrimSpace(text) == strings.TrimSpace(def),
		})
	}
	return out
}

// SetPrompt 保存自定义提示词。文本为空表示恢复默认。
func SetPrompt(role, text string) error {
	col := ""
	switch role {
	case RoleJudge:
		col = "judge_prompt"
	case RoleIdentify:
		col = "identify_prompt"
	default:
		return errors.New("未知的 agent 角色")
	}
	if len([]rune(text)) > 8000 {
		return errors.New("提示词过长（上限 8000 字）")
	}
	promptLock.Lock()
	defer promptLock.Unlock()
	if strings.TrimSpace(text) == "" {
		text = ""
	}
	return savePromptColumn(col, text)
}

// ResetPrompt 恢复默认（清空库里的自定义值）
func ResetPrompt(role string) error { return SetPrompt(role, "") }

// savePromptColumn 更新单行设置里的某一列；行不存在时先建。
func savePromptColumn(col, text string) error {
	var s model.AISettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		// 没有设置行：建一行，其余字段用默认值
		s = model.AISettings{ID: 1, ServiceIdentify: true, ScanMonitor: true}
		if err := config.DB.Create(&s).Error; err != nil {
			return err
		}
	}
	return config.DB.Model(&model.AISettings{}).Where("id = 1").Update(col, text).Error
}
