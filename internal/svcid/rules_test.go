package svcid

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile 在 t.TempDir 下写一个最小规则 JSON 文件
func writeRuleFile(t *testing.T, dir, name, kind string, n int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	var body string
	switch kind {
	case "web":
		body = "["
		for i := 0; i < n; i++ {
			if i > 0 {
				body += ","
			}
			body += `{"id":"` + name + `-` + string(rune('a'+i)) + `","info":{"name":"rule-` + name + `"},"http":[{"matchers":[{"type":"word","words":["zzz-unique-token-` + name + `"]}]}]}`
		}
		body += "]"
	default:
		body = "["
		for i := 0; i < n; i++ {
			if i > 0 {
				body += ","
			}
			body += `{"id":"` + name + `-` + string(rune('a'+i)) + `","info":{"name":"svc"},"tcp":[{"inputs":[{"data":"\\x01"}]}]}`
		}
		body += "]"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadCommaPathList -rules 逗号列表：目录 + 单个 .json 文件混用，按文件名归类
func TestLoadCommaPathList(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "a")
	dirB := filepath.Join(base, "b")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 目录 A：标准布局（web 单文件 + service 分片）
	writeRuleFile(t, dirA, "web_fingerprint_v4.json", "web", 2)
	writeRuleFile(t, dirA, "service_fingerprint_v4.part00.json", "svc", 3)
	writeRuleFile(t, dirA, "service_fingerprint_v4.part01.json", "svc", 4)
	// 目录 B：另一套 web 单文件 + service 单文件
	writeRuleFile(t, dirB, "web_fingerprint_v4.json", "web", 1)
	writeRuleFile(t, dirB, "service_fingerprint_v4.json", "svc", 5)
	// 独立文件（在第三个目录，模拟用户只指到某个文件）
	dirC := filepath.Join(base, "c")
	if err := os.MkdirAll(dirC, 0o755); err != nil {
		t.Fatal(err)
	}
	extraWeb := writeRuleFile(t, dirC, "my_web_fingerprint_v4.json", "web", 7)
	extraSvcPart := writeRuleFile(t, dirC, "service_fingerprint_v4.part09.json", "svc", 6)

	cases := []struct {
		name    string
		paths   string
		wantWeb int
		wantSvc int
		wantErr bool
	}{
		{"单目录（旧行为）", dirA, 2, 7, false},
		{"目录+目录", dirA + "," + dirB, 3, 12, false},
		{"目录带空格", " " + dirA + " , " + dirB + " ", 3, 12, false},
		{"只给 web 文件", extraWeb, 7, 0, false},
		{"只给 service 分片文件", extraSvcPart, 0, 6, true}, // 无 web 规则 → 明确报错
		{"文件+目录混用", extraWeb + "," + dirB, 8, 5, false},
		{"文件+分片文件", extraWeb + "," + extraSvcPart, 7, 6, false},
		{"重复路径只加载一次", dirA + "," + dirA + "," + filepath.Join(dirA, "web_fingerprint_v4.json"), 2, 7, false},
		{"路径不存在", filepath.Join(base, "nope"), 0, 0, true},
		{"目录内的 web 单文件（只加载 web）", filepath.Join(dirA, "web_fingerprint_v4.json"), 2, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs, err := Load(c.paths)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际成功 web=%d svc=%d", len(rs.Web), len(rs.Service))
				}
				return
			}
			if err != nil {
				t.Fatalf("Load 失败: %v", err)
			}
			if len(rs.Web) != c.wantWeb {
				t.Errorf("web 条数 = %d，期望 %d", len(rs.Web), c.wantWeb)
			}
			if len(rs.Service) != c.wantSvc {
				t.Errorf("service 条数 = %d，期望 %d", len(rs.Service), c.wantSvc)
			}
		})
	}
}

// TestLoadUnclassifiableFile 文件名既无 web 也无 service 前缀 → 报错并说明归类规则
func TestLoadUnclassifiableFile(t *testing.T) {
	dir := t.TempDir()
	p := writeRuleFile(t, dir, "whatever.json", "web", 1)
	if _, err := Load(p); err == nil {
		t.Fatal("期望无法归类报错")
	} else if !contains(err.Error(), "无法归类") {
		t.Errorf("错误信息不清晰: %v", err)
	}
}

// TestLoadBadJSON 规则文件坏 JSON → 报错带文件名
func TestLoadBadJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "web_fingerprint_v4.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("期望解析报错")
	} else if !contains(err.Error(), "web_fingerprint_v4.json") {
		t.Errorf("错误信息缺少文件名: %v", err)
	}
}

// TestLoadRealDataSmoke 仓库自带规则目录仍能加载（默认 -rules 行为回归）
func TestLoadRealDataSmoke(t *testing.T) {
	rs, err := Load("../data/fingerprint")
	if err != nil {
		t.Skipf("仓库规则数据不可用: %v", err)
	}
	if len(rs.Web) == 0 || len(rs.Service) == 0 {
		t.Fatalf("规则集为空: web=%d svc=%d", len(rs.Web), len(rs.Service))
	}
	// 同一批规则用文件形式列出，条数应与目录 glob 一致
	dir := filepath.Join("..", "data", "fingerprint")
	webFile := filepath.Join(dir, "web_fingerprint_v4.json")
	rs2, err := Load(webFile + "," + dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs2.Web) != len(rs.Web) || len(rs2.Service) != len(rs.Service) {
		t.Errorf("去重后条数不一致: web %d/%d svc %d/%d", len(rs2.Web), len(rs.Web), len(rs2.Service), len(rs.Service))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
