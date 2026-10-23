package service

import (
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	maxUserFails = 5                // 同一用户名连续失败次数
	maxIPFails   = 20               // 同一 IP 连续失败次数（防多账号枚举）
	lockDuration = 15 * time.Minute // 锁定时长
)

type failRec struct {
	count int
	until time.Time
}

var (
	userFails sync.Map // username -> *failRec
	ipFails   sync.Map // ip -> *failRec
)

func checkLock(m *sync.Map, key, label string) error {
	if key == "" {
		return nil
	}
	v, ok := m.Load(key)
	if !ok {
		return nil
	}
	r := v.(*failRec)
	// until 为零值 = 尚未达到锁定阈值，正常放行且不能删计数（否则每次失败都从 1 重新开始）
	if r.until.IsZero() {
		return nil
	}
	if r.until.After(time.Now()) {
		mins := int(time.Until(r.until).Minutes()) + 1
		return fmt.Errorf("%s连续错误过多，请 %d 分钟后再试", label, mins)
	}
	// 锁已过期，重置计数
	m.Delete(key)
	return nil
}

func recordFail(m *sync.Map, key string, max int) {
	if key == "" {
		return
	}
	v, _ := m.LoadOrStore(key, &failRec{})
	r := v.(*failRec)
	r.count++
	log.Printf("[loginlock] key=%q count=%d max=%d", key, r.count, max)
	if r.count >= max {
		r.until = time.Now().Add(lockDuration)
		log.Printf("[loginlock] key=%q 已锁定至 %s", key, r.until.Format("15:04:05"))
	}
}

// CheckLoginLock 检查用户名与来源 IP 是否处于锁定状态
func CheckLoginLock(username, ip string) error {
	if err := checkLock(&userFails, username, "密码"); err != nil {
		return err
	}
	return checkLock(&ipFails, ip, "登录")
}

// RecordLoginFail 记录一次登录失败
func RecordLoginFail(username, ip string) {
	recordFail(&userFails, username, maxUserFails)
	recordFail(&ipFails, ip, maxIPFails)
}

// ClearLoginFail 登录成功后清除计数
func ClearLoginFail(username, ip string) {
	if username != "" {
		userFails.Delete(username)
	}
	if ip != "" {
		ipFails.Delete(ip)
	}
}
