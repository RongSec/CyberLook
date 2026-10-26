package main

import (
	"fmt"
	"net"
	"time"
)

func main() {
	// 192.0.2.1 是 RFC5737 保留的测试地址，正常网络里绝不可达。
	// 如果它也能"连接成功"，说明是中间设备/网络层在伪造 SYN-ACK。
	targets := []string{
		"192.0.2.1", // 保留测试地址（重点看它）
		"192.0.2.2",
		"10.24.11.5",
		"192.0.2.1", // 保留测试地址
		"203.0.113.1",
	}
	ports := []string{"80", "443", "445", "22", "3389", "8080", "8443", "3306", "6379", "1433", "5432", "9200", "7001", "8888"}

	for _, ip := range targets {
		for _, port := range ports {
			start := time.Now()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), 800*time.Millisecond)
			ms := time.Since(start).Milliseconds()
			if err != nil {
				fmt.Printf("%-14s:%-4s FAIL  %dms  %v\n", ip, port, ms, err)
				continue
			}
			remote := conn.RemoteAddr().String()
			local := conn.LocalAddr().String()
			_ = conn.Close()
			fmt.Printf("%-14s:%-4s OK    %dms  remote=%s local=%s\n", ip, port, ms, remote, local)
		}
	}
}
