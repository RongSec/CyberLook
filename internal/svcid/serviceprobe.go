package svcid

// FingerprintHub service 指纹的主动探针执行器（全量 11938 条规则的落地使用）
//
// 这些规则全是 nmap 风格：一个 inputs[].data（要发的字节）+ 若干 extractors（正则）。
// 同一批规则常常共用同一个 probe（例如 X11 probe），所以按 (port, input) 分组：
// 一个端口一次连接、一次发包，就能批量验证该组下所有规则，避免 N 次发包。
//
// 安全边界：
//   - 只对**被动探针没识别出产品**的端口执行（减少无意义发包）
//   - 每个端口最多 MaxProbes 组（默认 3），且只取"port 字段命中本端口"的规则
//   - 单次连接 600ms 读超时，发完即关

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type svcRuleRef struct {
	ID      string
	Name    string
	Product string
	Res     []*regexp.Regexp
}

type svcProbeGroup struct {
	Data      []byte
	Refs      []svcRuleRef
	N         int
	OnlyPort  int    // 0=端口串含区间；非 0=端口串里只有这一个端口（专属 probe）
	ProbeName string // probe 名（如 smbprog-neg）
}

// probeIndex port -> 去重后的 probe 分组
type ProbeIndex struct {
	byPort map[int][]*svcProbeGroup
	mu     sync.RWMutex
}

// BuildServiceProbes 编译全量 service 规则为可执行的主动探针索引
func (rs *RuleSet) BuildServiceProbes() *ProbeIndex {
	idx := &ProbeIndex{byPort: map[int][]*svcProbeGroup{}}
	// key: port|base64(input) —— 同一端口同一 input 合并成一个 group
	type gkey struct {
		port int
		data string
	}
	groups := map[gkey]*svcProbeGroup{}

	for i := range rs.Service {
		r := &rs.Service[i]
		name := strings.TrimSpace(r.Info.Name)
		if name == "" {
			name = r.ID
		}
		product := asString(r.Info.Metadata["product"])
		if product == "" {
			product = name
		}
		for _, it := range r.TCP {
			if len(it.Inputs) == 0 || it.Port == "" {
				continue // 无 probe 或无端口约束的规则不主动发包
			}
			ports := parsePortSpec(it.Port)
			if len(ports) == 0 {
				continue
			}
			data := decodeEscaped(it.Inputs[0].Data)
			if len(data) == 0 {
				continue
			}
			onlyPort := 0
			if len(ports) == 1 {
				onlyPort = ports[0]
			}
			var res []*regexp.Regexp
			for _, ex := range it.Extractors {
				if ex.Type != "regex" {
					continue
				}
				for _, pat := range ex.Regex {
					if pat == "" {
						continue
					}
					if re, err := regexp.Compile("(?i)" + pat); err == nil {
						res = append(res, re)
					}
				}
			}
			if len(res) == 0 {
				continue
			}
			ref := svcRuleRef{ID: r.ID, Name: name, Product: product, Res: res}
			for _, p := range ports {
				k := gkey{port: p, data: string(data)}
				g := groups[k]
				if g == nil {
					g = &svcProbeGroup{Data: data, OnlyPort: onlyPort, ProbeName: it.Name}
					groups[k] = g
				}
				g.Refs = append(g.Refs, ref)
				g.N++
			}
		}
	}
	for k, g := range groups {
		idx.byPort[k.port] = append(idx.byPort[k.port], g)
	}
	for p := range idx.byPort {
		sort.Slice(idx.byPort[p], func(i, j int) bool { return idx.byPort[p][i].N > idx.byPort[p][j].N })
	}
	return idx
}

// RunServiceProbes 对一个端口跑主动探针，返回命中的产品与证据
func (idx *ProbeIndex) RunServiceProbes(ctx context.Context, ip string, port int, maxProbes int) (product, evidence string, ok bool) {
	if idx == nil {
		return "", "", false
	}
	if maxProbes <= 0 {
		maxProbes = 3
	}
	idx.mu.RLock()
	groups := append([]*svcProbeGroup(nil), idx.byPort[port]...)
	idx.mu.RUnlock()
	// 排序：端口字段只含本端口的（专属 probe）优先，其次协议名匹配，最后规则数
	hint := portHints[port]
	sort.SliceStable(groups, func(i, j int) bool {
		si, sj := groupScore(groups[i], port, hint), groupScore(groups[j], port, hint)
		if si != sj {
			return si > sj
		}
		return groups[i].N > groups[j].N
	})
	if len(groups) > maxProbes {
		groups = groups[:maxProbes]
	}

	for _, g := range groups {
		d := net.Dialer{Timeout: 800 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_, _ = conn.Write(g.Data)
		_ = conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
		buf := make([]byte, 8192)
		total := 0
		for total < len(buf) {
			m, err := conn.Read(buf[total:])
			if m > 0 {
				total += m
			}
			if err != nil {
				break
			}
			_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		}
		_ = conn.Close()
		if total <= 0 {
			continue
		}
		n := total
		// 二进制响应 → latin1 字符串，否则 RE2 的 \xNN 匹配不到非法 UTF-8 字节
		resp := latin1String(buf[:n])
		for _, ref := range g.Refs {
			for _, re := range ref.Res {
				if re.MatchString(resp) {
					return normalizeProduct(ref.Product), fmt.Sprintf("%s(%s)", ref.ID, ref.Name), true
				}
			}
		}
	}
	return "", "", false
}

// parsePortSpec 解析 "80,443,6000-6020" 这类端口串
func parsePortSpec(spec string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			lo, e1 := strconv.Atoi(part[:i])
			hi, e2 := strconv.Atoi(part[i+1:])
			if e1 != nil || e2 != nil || lo <= 0 || hi < lo || hi > 65535 || hi-lo > 1024 {
				continue // 过宽区间跳过，避免爆炸
			}
			for p := lo; p <= hi; p++ {
				out = append(out, p)
			}
			continue
		}
		if p, e := strconv.Atoi(part); e == nil && p > 0 && p <= 65535 {
			out = append(out, p)
		}
	}
	return out
}

// decodeEscaped 把 "\\x6C\\0\\x0B" 这种规则字面量还原成真实字节
func decodeEscaped(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out = append(out, s[i])
			continue
		}
		i++
		c := s[i]
		switch c {
		case 'x', 'X':
			if i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					out = append(out, byte(v))
					i += 2
					continue
				}
			}
			out = append(out, c)
		case '0':
			out = append(out, 0)
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case '\\':
			out = append(out, '\\')
		default:
			out = append(out, c)
		}
	}
	return out
}

// PortCount 有多少个端口有主动探针规则
func (idx *ProbeIndex) PortCount() int {
	if idx == nil {
		return 0
	}
	return len(idx.byPort)
}

// groupScore probe 分组优先级：专属端口 > 协议名匹配 > 覆盖规则多
func groupScore(g *svcProbeGroup, port int, hint string) int {
	score := 0
	if g.OnlyPort == port {
		score += 100
	}
	if hint != "" && strings.Contains(strings.ToLower(g.ProbeName), strings.ToLower(hint)) {
		score += 50
	}
	score += g.N / 10
	return score
}

// latin1String 把字节流按 latin1 映射为字符串，让 \xNN 正则能匹配二进制
func latin1String(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}
