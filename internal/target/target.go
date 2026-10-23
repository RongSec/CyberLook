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
	excludeSet := map[string]bool{}
	for _, e := range excludes {
		for _, ip := range Expand(e) {
			excludeSet[ip] = true
		}
	}
	var out []string
	for ip := range targetSet {
		if !excludeSet[ip] {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out
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

// expandWildcard 支持 10.24.1.* / 10.24.*.* / 10.*.*.* 这类通配写法
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
