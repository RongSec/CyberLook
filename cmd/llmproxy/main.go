// llmproxy 宿主机侧应用层代理
//
// 用途：Docker Desktop for Windows 等环境下，容器访问内网 HTTPS 网关时
// TLS 握手会被中断（TCP 可达但读到 0 字节）。本代理跑在宿主机上，
// 由它完成 TLS 握手，容器只走明文 HTTP。
//
// 用法：
//   llmproxy.exe -listen :8898
// 然后在 LLM 配置里填「应用层代理」= http://host.docker.internal:8898
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

var legacyCiphers = []uint16{
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
}

type fetchReq struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type fetchResp struct {
	Status int               `json:"status"`
	Body   string            `json:"body"`
	Error  string            `json:"error,omitempty"`
}

func main() {
	listen := flag.String("listen", ":8898", "监听地址")
	flag.Parse()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS10,
				MaxVersion:         tls.VersionTLS13,
				CipherSuites:       legacyCiphers,
			},
		},
		Timeout: 180 * time.Second,
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})

	http.HandleFunc("/fetch", func(w http.ResponseWriter, r *http.Request) {
		var fr fetchReq
		if err := json.NewDecoder(r.Body).Decode(&fr); err != nil {
			json.NewEncoder(w).Encode(fetchResp{Error: "参数错误: " + err.Error()})
			return
		}
		log.Printf("[fetch] %s %s", fr.Method, shortURL(fr.URL))

		req, err := http.NewRequest(fr.Method, fr.URL, bytes.NewReader([]byte(fr.Body)))
		if err != nil {
			json.NewEncoder(w).Encode(fetchResp{Error: err.Error()})
			return
		}
		for k, v := range fr.Headers {
			req.Header.Set(k, v)
		}

		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[fetch] 失败 %v", err)
			json.NewEncoder(w).Encode(fetchResp{Error: err.Error()})
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		log.Printf("[fetch] %d, %d bytes, %dms", resp.StatusCode, len(body), time.Since(start).Milliseconds())

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fetchResp{Status: resp.StatusCode, Body: string(body)})
	})

	log.Printf("[llmproxy] 监听 %s，容器内配置「应用层代理」= http://host.docker.internal%s", *listen, *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

func shortURL(u string) string {
	if len(u) > 100 {
		return u[:100] + "..."
	}
	return u
}

var _ = fmt.Sprintf
