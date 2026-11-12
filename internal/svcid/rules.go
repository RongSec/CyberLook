// Package svcid 服务识别：FingerprintHub 全量规则匹配 + fingerprintx 协议识别
//
// 规则来源：0x727/FingerprintHub（MIT）
//   - web_fingerprint_v4.json     3450 条（word 3081 / regex 503 / favicon 286），被动匹配
//   - service_fingerprint_v4.json 11938 条，全部是主动探针（inputs + extractors），无 matcher
//
// 本文件实现 web 规则的**全量**加载与匹配（不裁剪）。service 规则全部载入索引，
// 但主动探针的执行单独开关控制（对生产服务发包有副作用）。
package svcid

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// WebMatcher 一条 web 规则里的单个匹配器
type WebMatcher struct {
	Type            string   `json:"type"`
	Words           []string `json:"words"`
	Regex           []string `json:"regex"`
	Part            string   `json:"part"`
	CaseInsensitive bool     `json:"case-insensitive"`

	// 编译产物
	res      []*regexp.Regexp
	lowered  []string
	hashVals []string // favicon: 支持 md5/mmh3 字符串
}

// WebRule 一条 web 指纹规则
type WebRule struct {
	ID      string        `json:"id"`
	Info    RuleInfo      `json:"info"`
	HTTP    []HTTPMatcher `json:"http"`
	Name    string        `json:"-"`
	Product string        `json:"-"`
	Vendor  string        `json:"-"`
}

type RuleInfo struct {
	Name     string         `json:"name"`
	Metadata map[string]any `json:"metadata"`
}

type HTTPMatcher struct {
	Method            string       `json:"method"`
	Path              []string     `json:"path"`
	Matchers          []WebMatcher `json:"matchers"`
	MatchersCondition string       `json:"matchers-condition"`
}

// SvcRule service 指纹（主动探针型）
type SvcExtractor struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Regex []string `json:"regex"`
}

type SvcInput struct {
	Data string `json:"data"`
}

type SvcTCPItem struct {
	Name       string         `json:"name"`
	Inputs     []SvcInput     `json:"inputs"`
	Host       []string       `json:"host"`
	Port       string         `json:"port"`
	Extractors []SvcExtractor `json:"extractors"`
}

type SvcRule struct {
	ID   string       `json:"id"`
	Info RuleInfo     `json:"info"`
	TCP  []SvcTCPItem `json:"tcp"`
}

// Hit 一次命中
type Hit struct {
	RuleID  string
	Name    string
	Product string
	Vendor  string
	Source  string // web / service
	Score   int
}

// RuleSet 全量规则集
type RuleSet struct {
	Web     []WebRule
	Service []SvcRule

	ac       *acAutomaton
	patRule  []int32 // pattern 序号 → 规则序号
	patMatch []int32 // pattern 序号 → matcher 序号（规则内）

	// favicon md5 → 规则序号
	faviconIndex map[string][]int32

	LoadMs int64
}

// Load 加载 FingerprintHub 规则。
//
// paths 是逗号分隔的路径列表，每一项既可以是目录，也可以是 .json 文件：
//   - 目录：读取该目录下的 web_fingerprint_v4.json，并按 glob
//     service_fingerprint_v4*.json 收集 service 规则（单文件与 partNN 分片共存时全部累加）
//   - .json 文件：按文件名判断归属——名字含 "web_fingerprint" 的进 Web 规则集，
//     含 "service_fingerprint" 的进 Service 规则集（单文件与 partNN 分片同等对待）
//
// 同一文件被多个路径重复命中时只加载一次。Load("data/fingerprint") 与旧版
// 单目录调用行为完全一致。
func Load(paths string) (*RuleSet, error) {
	webFiles, svcFiles, err := collectRuleFiles(paths)
	if err != nil {
		return nil, err
	}

	rs := &RuleSet{faviconIndex: map[string][]int32{}}

	for _, webPath := range webFiles {
		b, err := os.ReadFile(webPath)
		if err != nil {
			return nil, fmt.Errorf("读取 %s: %w", webPath, err)
		}
		var chunk []WebRule
		if err := json.Unmarshal(b, &chunk); err != nil {
			return nil, fmt.Errorf("解析 web 规则 %s: %w", filepath.Base(webPath), err)
		}
		rs.Web = append(rs.Web, chunk...)
	}
	if len(rs.Web) == 0 {
		return nil, fmt.Errorf("未加载到任何 web 规则（-rules %s：目录需含 web_fingerprint_v4.json，文件需文件名含 web_fingerprint）", paths)
	}

	// service 规则：支持单文件或分片（service_fingerprint_v4.part00.json ...）
	for _, svcPath := range svcFiles {
		b, err := os.ReadFile(svcPath)
		if err != nil {
			continue
		}
		var chunk []SvcRule
		if err := json.Unmarshal(b, &chunk); err != nil {
			return nil, fmt.Errorf("解析 service 规则 %s: %w", filepath.Base(svcPath), err)
		}
		rs.Service = append(rs.Service, chunk...)
	}

	// 预处理：取名称/产品，收集 word pattern 建 AC
	var patterns []string
	for i := range rs.Web {
		r := &rs.Web[i]
		r.Name = strings.TrimSpace(r.Info.Name)
		if r.Name == "" {
			r.Name = r.ID
		}
		if r.Info.Metadata != nil {
			r.Product = asString(r.Info.Metadata["product"])
			r.Vendor = asString(r.Info.Metadata["vendor"])
		}
		if r.Product == "" {
			r.Product = r.Name
		}
		mid := 0 // 规则内 matcher 全局序号（跨 http item 连续编号）
		for mi := range r.HTTP {
			it := &r.HTTP[mi]
			if it.MatchersCondition == "" {
				it.MatchersCondition = "and"
			}
			for wi := range it.Matchers {
				m := &it.Matchers[wi]
				mid++
				switch m.Type {
				case "word":
					for _, w := range m.Words {
						w = strings.TrimSpace(w)
						if w == "" {
							continue
						}
						if m.CaseInsensitive {
							w = strings.ToLower(w)
						}
						patterns = append(patterns, w)
						rs.patRule = append(rs.patRule, int32(i))
						rs.patMatch = append(rs.patMatch, int32(mid))
					}
				case "regex":
					for _, re := range m.Regex {
						if re == "" {
							continue
						}
						if p, err := regexp.Compile("(?i)" + re); err == nil {
							m.res = append(m.res, p)
						}
					}
				case "favicon":
					for _, w := range m.Words {
						rs.faviconIndex[strings.ToLower(strings.TrimSpace(w))] = append(rs.faviconIndex[strings.ToLower(strings.TrimSpace(w))], int32(i))
					}
					for _, re := range m.Regex {
						m.hashVals = append(m.hashVals, re)
					}
				}
			}
		}
	}

	rs.ac = newAC(patterns)
	return rs, nil
}

// collectRuleFiles 把逗号分隔的路径列表展开成 (web 文件, service 文件) 两组绝对路径。
// 目录项按 glob 收集，.json 文件项按文件名归类。
func collectRuleFiles(paths string) (webFiles, svcFiles []string, err error) {
	items := strings.Split(paths, ",")
	seen := map[string]bool{}
	add := func(dst *[]string, p string) {
		abs, e := filepath.Abs(p)
		if e != nil {
			abs = filepath.Clean(p)
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		*dst = append(*dst, abs)
	}

	for _, raw := range items {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		fi, e := os.Stat(p)
		if e != nil {
			return nil, nil, fmt.Errorf("规则路径 %s 不可用: %w", p, e)
		}
		if fi.IsDir() {
			// 目录：保持 glob 行为，单文件与 partNN 分片都能命中
			w, _ := filepath.Glob(filepath.Join(p, "web_fingerprint_v4*.json"))
			for _, f := range w {
				add(&webFiles, f)
			}
			s, _ := filepath.Glob(filepath.Join(p, "service_fingerprint_v4*.json"))
			for _, f := range s {
				add(&svcFiles, f)
			}
			continue
		}
		// 文件：按文件名判断归属
		base := strings.ToLower(filepath.Base(p))
		switch {
		case strings.Contains(base, "web_fingerprint"):
			add(&webFiles, p)
		case strings.Contains(base, "service_fingerprint"):
			add(&svcFiles, p)
		default:
			return nil, nil, fmt.Errorf("规则文件 %s 无法归类：文件名需含 web_fingerprint 或 service_fingerprint", p)
		}
	}

	if len(webFiles) == 0 && len(svcFiles) == 0 {
		return nil, nil, fmt.Errorf("路径列表 %s 中没有规则文件", paths)
	}
	sort.Strings(webFiles)
	sort.Strings(svcFiles)
	return webFiles, svcFiles, nil
}

// MatchWeb 对一个 HTTP 响应做全量规则匹配，返回命中的规则（按分数排序）
func (rs *RuleSet) MatchWeb(headers, body, title string, faviconMMH3 string, faviconMD5 string) []Hit {
	lowerHeaders := strings.ToLower(headers)
	lowerBody := strings.ToLower(body)
	lowerTitle := strings.ToLower(title)

	// 一次 AC 扫描，拿到所有命中的 pattern
	full := lowerHeaders + "\n" + lowerTitle + "\n" + lowerBody
	matchedPats := rs.ac.Match(full)

	// pattern → (rule, matcher) 命中集合
	hitMatcher := map[[2]int32]bool{}
	for _, pi := range matchedPats {
		hitMatcher[[2]int32{rs.patRule[pi], rs.patMatch[pi]}] = true
	}

	var hits []Hit
	for i := range rs.Web {
		r := &rs.Web[i]
		if len(r.HTTP) == 0 {
			continue
		}
		ok := false
		score := 0
		mid := 0
		for _, it := range r.HTTP {
			pass, sc := evalMatchers(int32(i), it, &mid, hitMatcher, headers, lowerHeaders, body, lowerBody, title, lowerTitle, faviconMMH3, faviconMD5)
			if pass {
				ok = true
				score += sc
			}
		}
		if ok {
			hits = append(hits, Hit{RuleID: r.ID, Name: r.Name, Product: r.Product, Vendor: r.Vendor, Source: "web", Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits
}

func evalMatchers(ruleIdx int32, it HTTPMatcher, midPtr *int, hit map[[2]int32]bool, headers, lowerHeaders, body, lowerBody, title, lowerTitle, favMMH3, favMD5 string) (bool, int) {
	passed := 0
	total := 0
	score := 0

	for _, m := range it.Matchers {
		*midPtr = *midPtr + 1
		mid := int32(*midPtr)
		total++
		ok := false
		switch m.Type {
		case "word":
			if hit[[2]int32{ruleIdx, mid}] {
				ok = true
			}
		case "regex":
			hay := lowerBody
			switch m.Part {
			case "header":
				hay = lowerHeaders
			case "title":
				hay = lowerTitle
			case "all":
				hay = lowerHeaders + "\n" + lowerTitle + "\n" + lowerBody
			}
			for _, re := range m.res {
				if re.MatchString(hay) {
					ok = true
					break
				}
			}
		case "favicon":
			// 规则里 favicon 的 words 是 hash 字符串（md5 或 mmh3）
			for _, w := range m.Words {
				w = strings.ToLower(strings.TrimSpace(w))
				if w != "" && (w == strings.ToLower(favMMH3) || w == strings.ToLower(favMD5)) {
					ok = true
					break
				}
			}
		}
		if ok {
			passed++
			score += 10
			if m.Type == "favicon" {
				score += 30
			}
			if m.Type == "regex" {
				score += 5
			}
		}
	}
	if total == 0 {
		return false, 0
	}
	if it.MatchersCondition == "or" {
		return passed > 0, score
	}
	return passed == total, score
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// FaviconHashes 计算 favicon 的 md5（mmh3 由调用方传入）
func FaviconHashes(b []byte) (md5hex string) {
	if len(b) == 0 {
		return ""
	}
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}

// ===== Aho-Corasick =====

type acNode struct {
	next map[byte]int32
	fail int32
	out  []int32
}

type acAutomaton struct {
	nodes []acNode
}

func newAC(patterns []string) *acAutomaton {
	a := &acAutomaton{nodes: []acNode{{next: map[byte]int32{}}}}
	for pi, p := range patterns {
		cur := int32(0)
		for i := 0; i < len(p); i++ {
			c := p[i]
			nxt, ok := a.nodes[cur].next[c]
			if !ok {
				a.nodes = append(a.nodes, acNode{next: map[byte]int32{}})
				nxt = int32(len(a.nodes) - 1)
				a.nodes[cur].next[c] = nxt
			}
			cur = nxt
		}
		a.nodes[cur].out = append(a.nodes[cur].out, int32(pi))
	}
	// BFS 构建 fail 指针
	queue := make([]int32, 0, len(a.nodes))
	for _, v := range a.nodes[0].next {
		a.nodes[v].fail = 0
		queue = append(queue, v)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for c, nxt := range a.nodes[cur].next {
			f := a.nodes[cur].fail
			for f != 0 {
				if t, ok := a.nodes[f].next[c]; ok {
					f = t
					goto setFail
				}
				f = a.nodes[f].fail
			}
			if t, ok := a.nodes[0].next[c]; ok && t != nxt {
				f = t
			}
		setFail:
			a.nodes[nxt].fail = f
			a.nodes[nxt].out = append(a.nodes[nxt].out, a.nodes[f].out...)
			queue = append(queue, nxt)
		}
	}
	return a
}

// Match 返回命中的 pattern 序号
func (a *acAutomaton) Match(text string) []int32 {
	var out []int32
	cur := int32(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		for {
			if nxt, ok := a.nodes[cur].next[c]; ok {
				cur = nxt
				break
			}
			if cur == 0 {
				break
			}
			cur = a.nodes[cur].fail
		}
		if len(a.nodes[cur].out) > 0 {
			out = append(out, a.nodes[cur].out...)
		}
	}
	return out
}

// Stats 规则规模统计
func (rs *RuleSet) Stats() map[string]int {
	fav := 0
	rgx := 0
	for i := range rs.Web {
		for _, it := range rs.Web[i].HTTP {
			for _, m := range it.Matchers {
				switch m.Type {
				case "favicon":
					fav++
				case "regex":
					rgx++
				}
			}
		}
	}
	return map[string]int{
		"webRules":     len(rs.Web),
		"serviceRules": len(rs.Service),
		"faviconRules": fav,
		"regexRules":   rgx,
		"patterns":     len(rs.patRule),
		"acNodes":      len(rs.ac.nodes),
	}
}
