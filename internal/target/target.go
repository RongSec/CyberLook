package target

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

const maxExpand = 65536 // 单目标展开上限

// Resolve 展开目标 + 去重 + 去除避开范围，返回排序后的最终 IP 列表
func Resolve(targets, excludes []string) []string {
	targetSet := map[string]bool{}
	for _, t := range targets {
		for _, ip := range Expand(t) {
			targetSet[ip] = true
		}
	}
	ex := NewExcluder(excludes)
	var out []string
	for ip := range targetSet {
		if !ex.Contains(ip) {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
}

// Excluder 免扫描匹配器：支持三类写法，语义分工明确
//
//	198.18.4.0/24        CIDR          → 前缀匹配（不展开，几万个 IP 也不占内存）
//	198.18.4.10-20       范围          → 展开成精确集合
//	*.*.*.1            通配          → 按八位组匹配（网段网关/交换机）
//	198.18.4.*           通配          → 同上，等价于 /24
//
// 为什么不能沿用「先把排除项 Expand 成 IP 集合」的老做法：
// Expand 对 `*.*.*.1` 会算出 0.0.0.1/0，前缀位数 < 16 时直接原样返回字符串，
// 于是这条排除**永远匹配不上任何 IP**（表现为"清单里有一条，但 .1 照样被扫"）。
type Excluder struct {
	set      map[string]bool
	prefixes []netip.Prefix
	wild     [][4]string
}

// NewExcluder 构建匹配器；空输入返回空匹配器（Contains 恒 false）。
func NewExcluder(patterns []string) *Excluder {
	e := &Excluder{set: map[string]bool{}}
	for _, raw := range patterns {
		p := strings.Trim(raw, " \t\r\n,，;；、")
		if p == "" {
			continue
		}
		switch {
		case strings.Contains(p, "*"):
			if w, ok := parseWildcard(p); ok {
				e.wild = append(e.wild, w)
			}
		case strings.Contains(p, "/"):
			if pr, err := netip.ParsePrefix(p); err == nil {
				e.prefixes = append(e.prefixes, pr.Masked())
			} else {
				e.set[p] = true
			}
		default:
			for _, ip := range Expand(p) {
				e.set[ip] = true
			}
		}
	}
	return e
}

// Contains 判定该 IP 是否落在排除项里。
func (e *Excluder) Contains(ip string) bool {
	if e == nil {
		return false
	}
	if len(e.set) == 0 && len(e.prefixes) == 0 && len(e.wild) == 0 {
		return false
	}
	if e.set[ip] {
		return true
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, pr := range e.prefixes {
		if pr.Contains(addr) {
			return true
		}
	}
	if len(e.wild) == 0 || !addr.Is4() {
		return false
	}
	b := addr.As4()
	octets := [4]string{strconv.Itoa(int(b[0])), strconv.Itoa(int(b[1])),
		strconv.Itoa(int(b[2])), strconv.Itoa(int(b[3]))}
	for _, w := range e.wild {
		hit := true
		for i := 0; i < 4; i++ {
			if w[i] != "*" && w[i] != octets[i] {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// parseWildcard 解析 `*.*.*.1` / `198.18.4.*` 这类四段通配。
func parseWildcard(p string) ([4]string, bool) {
	var out [4]string
	parts := strings.Split(p, ".")
	if len(parts) != 4 {
		return out, false
	}
	for i, seg := range parts {
		if seg == "*" {
			out[i] = "*"
			continue
		}
		n, err := strconv.Atoi(seg)
		if err != nil || n < 0 || n > 255 {
			return out, false
		}
		out[i] = strconv.Itoa(n)
	}
	return out, true
}

// Expand 展开单个目标（IP / CIDR / 范围）成 IP 列表
func Expand(t string) []string {
	t = strings.Trim(t, " \t\r\n,，;；、")
	if t == "" {
		return nil
	}
	if strings.Contains(t, "*") {
		return expandWildcard(t)
	}
	if strings.Contains(t, "/") {
		return expandCIDR(t)
	}
	if strings.Contains(t, "-") {
		return expandRange(t)
	}
	return []string{t}
}

// expandWildcard 支持 198.18.1.* / 198.18.*.* / 10.*.*.* 这类通配写法
func expandWildcard(t string) []string {
	parts := strings.Split(t, ".")
	if len(parts) != 4 {
		return []string{t}
	}
	bits := 32
	ipParts := make([]string, 4)
	for i, p := range parts {
		if p == "*" {
			if bits == 32 {
				bits = i * 8
			}
			ipParts[i] = "0"
		} else {
			ipParts[i] = p
		}
	}
	cidr := strings.Join(ipParts, ".") + "/" + strconv.Itoa(bits)
	return expandCIDR(cidr)
}

func expandCIDR(t string) []string {
	prefix, err := netip.ParsePrefix(t)
	if err != nil {
		return []string{t}
	}
	// 超大网段（/16 以下）不展开，原样保留交给工具
	if prefix.Bits() < 16 {
		return []string{t}
	}
	var ips []string
	addr := prefix.Masked().Addr()
	for prefix.Contains(addr) && len(ips) <= maxExpand {
		ips = append(ips, addr.String())
		next := addr.Next()
		if !prefix.Contains(next) {
			break
		}
		addr = next
	}
	return ips
}

func expandRange(t string) []string {
	parts := strings.SplitN(t, "-", 2)
	if len(parts) != 2 {
		return []string{t}
	}
	start, err := netip.ParseAddr(strings.TrimSpace(parts[0]))
	if err != nil {
		return []string{t}
	}
	endStr := strings.TrimSpace(parts[1])
	var end netip.Addr
	if endStr == "" {
		// 缺省结束：同网段到 .254
		b := start.As4()
		end = netip.AddrFrom4([4]byte{b[0], b[1], b[2], 254})
	} else if num, err := strconv.Atoi(endStr); err == nil {
		b := start.As4()
		end = netip.AddrFrom4([4]byte{b[0], b[1], b[2], byte(num)})
	} else if e, err := netip.ParseAddr(endStr); err == nil {
		end = e
	} else {
		return []string{t}
	}
	var ips []string
	for addr := start; addr.Compare(end) <= 0 && len(ips) <= maxExpand; addr = addr.Next() {
		ips = append(ips, addr.String())
		if addr == end {
			break
		}
	}
	return ips
}
