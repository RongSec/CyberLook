package service

import (
	"asset-platform/internal/config"

	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// NotifySettings 通知配置（对应 notify_settings 单行表）
type NotifySettings struct {
	ID              uint   `gorm:"primaryKey;column:id" json:"-"`
	WecomWebhook    string `gorm:"column:wecom_webhook" json:"wecomWebhook"`
	DingtalkWebhook string `gorm:"column:dingtalk_webhook" json:"dingtalkWebhook"`
	FeishuWebhook   string `gorm:"column:feishu_webhook" json:"feishuWebhook"`
	GenericWebhook  string `gorm:"column:generic_webhook" json:"genericWebhook"`
	EmailSMTP       string `gorm:"column:email_smtp" json:"emailSmtp"`
	EmailPort       int    `gorm:"column:email_port" json:"emailPort"`
	EmailSSL        bool   `gorm:"column:email_ssl" json:"emailSsl"`
	EmailUser       string `gorm:"column:email_user" json:"emailUser"`
	EmailPass       string `gorm:"column:email_pass" json:"-"`
	EmailTo         string `gorm:"column:email_to" json:"emailTo"`

	OnTaskDone   bool `gorm:"column:on_task_done" json:"onTaskDone"`
	OnNewAsset   bool `gorm:"column:on_new_asset" json:"onNewAsset"`
	OnViolation  bool `gorm:"column:on_violation" json:"onViolation"`
}

func (NotifySettings) TableName() string { return "notify_settings" }

var notifyClient = &http.Client{Timeout: 12 * time.Second}

func postJSON(url string, payload interface{}) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("webhook 地址为空")
	}
	body, _ := json.Marshal(payload)
	resp, err := notifyClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// SendWecom 企业微信群机器人（markdown）
func SendWecom(webhook, title, content string) error {
	return postJSON(webhook, map[string]interface{}{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": "**" + title + "**\n" + content},
	})
}

// SendDingtalk 钉钉群机器人（markdown）
func SendDingtalk(webhook, title, content string) error {
	return postJSON(webhook, map[string]interface{}{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": title, "text": "### " + title + "\n" + content},
	})
}

// SendFeishu 飞书群机器人（text）
func SendFeishu(webhook, title, content string) error {
	return postJSON(webhook, map[string]interface{}{
		"msg_type": "text",
		"content":  map[string]string{"text": title + "\n" + content},
	})
}

// SendGeneric 通用 webhook（JSON: {title, content}）
func SendGeneric(webhook, title, content string) error {
	return postJSON(webhook, map[string]string{"title": title, "content": content})
}

// SendEmail 邮件通知（支持 465 SSL / 587 STARTTLS）
func SendEmail(s NotifySettings, title, content string) error {
	if s.EmailSMTP == "" || s.EmailUser == "" || s.EmailTo == "" {
		return fmt.Errorf("邮件配置不完整")
	}
	port := s.EmailPort
	if port == 0 {
		port = 465
	}
	addr := fmt.Sprintf("%s:%d", s.EmailSMTP, port)
	to := strings.Split(s.EmailTo, ",")
	msg := strings.Join([]string{
		"From: " + s.EmailUser,
		"To: " + strings.Join(to, ","),
		"Subject: " + title,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		content,
	}, "\r\n")

	auth := smtp.PlainAuth("", s.EmailUser, s.EmailPass, s.EmailSMTP)
	if s.EmailSSL || port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.EmailSMTP})
		if err != nil {
			return err
		}
		client, err := smtp.NewClient(conn, s.EmailSMTP)
		if err != nil {
			return err
		}
		defer client.Close()
		if err := client.Auth(auth); err != nil {
			return err
		}
		if err := client.Mail(s.EmailUser); err != nil {
			return err
		}
		for _, r := range to {
			if err := client.Rcpt(strings.TrimSpace(r)); err != nil {
				return err
			}
		}
		w, err := client.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(msg)); err != nil {
			return err
		}
		return w.Close()
	}
	return smtp.SendMail(addr, auth, s.EmailUser, to, []byte(msg))
}

// Broadcast 按配置向所有已启用的渠道发送，返回各渠道结果
func Broadcast(title, content string) map[string]string {
	var s NotifySettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		return map[string]string{"error": "通知配置读取失败"}
	}
	results := map[string]string{}
	send := func(name string, fn func() error) {
		if err := fn(); err != nil {
			results[name] = "✗ " + err.Error()
		} else {
			results[name] = "✓ 已发送"
		}
	}
	if s.WecomWebhook != "" {
		send("企业微信", func() error { return SendWecom(s.WecomWebhook, title, content) })
	}
	if s.DingtalkWebhook != "" {
		send("钉钉", func() error { return SendDingtalk(s.DingtalkWebhook, title, content) })
	}
	if s.FeishuWebhook != "" {
		send("飞书", func() error { return SendFeishu(s.FeishuWebhook, title, content) })
	}
	if s.GenericWebhook != "" {
		send("Webhook", func() error { return SendGeneric(s.GenericWebhook, title, content) })
	}
	if s.EmailSMTP != "" {
		send("邮件", func() error { return SendEmail(s, title, content) })
	}
	return results
}

// NotifyTaskDone 任务完成通知（异步，失败不影响主流程）
func NotifyTaskDone(taskID uint, name, status string) {
	var s NotifySettings
	if err := config.DB.First(&s, 1).Error; err == nil && !s.OnTaskDone {
		return
	}
	go func() {
		title := fmt.Sprintf("扫描任务%s：%s", status, name)
		content := fmt.Sprintf("任务 #%d\n状态：%s\n时间：%s", taskID, status, time.Now().Format("2006-01-02 15:04:05"))
		for k, v := range Broadcast(title, content) {
			if strings.HasPrefix(v, "✗") {
				fmt.Printf("[notify] %s 发送失败: %s\n", k, v)
			}
		}
	}()
}
