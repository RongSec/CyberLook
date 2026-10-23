package similarity

import "testing"

func TestFingerprintSensitivity(t *testing.T) {
	base := `<html><body><div class="login"><h1>泛微协同办公平台</h1><p>请输入用户名和密码登录</p><form><input name="user"/><input name="pass"/><button>登录</button></form><footer>泛微网络科技 版权所有</footer></div></body></html>`
	// 同一系统小改版：多一行公告
	same := `<html><body><div class="login"><h1>泛微协同办公平台</h1><p>请输入用户名和密码登录</p><form><input name="user"/><input name="pass"/><button>登录</button></form><p>系统将于今晚维护</p><footer>泛微网络科技 版权所有</footer></div></body></html>`
	// 完全不同的系统
	other := `<html><body><div><h1>华为防火墙 USG6000</h1><p>设备管理控制台</p><ul><li>安全策略</li><li>流量监控</li><li>日志审计</li></ul><footer>Huawei Technologies</footer></div></body></html>`

	fb, fs, fo := Fingerprint(base), Fingerprint(same), Fingerprint(other)
	if fb == 0 || fs == 0 || fo == 0 {
		t.Fatal("指纹不应为 0")
	}
	dSame := Hamming(fb, fs)
	dOther := Hamming(fb, fo)
	t.Logf("同一系统小改版 距离=%d, 完全不同系统 距离=%d", dSame, dOther)

	if dSame >= dOther {
		t.Errorf("相似页面距离(%d) 应显著小于无关页面(%d)", dSame, dOther)
	}
	if Changed(fb, fs, 0) {
		t.Errorf("小改版不应判定为变更（距离=%d，阈值=%d）", dSame, DefaultThreshold)
	}
	if !Changed(fb, fo, 0) {
		t.Errorf("换系统应判定为变更（距离=%d，阈值=%d）", dOther, DefaultThreshold)
	}
}

func TestFingerprintEmpty(t *testing.T) {
	if Fingerprint("") != 0 {
		t.Error("空文本指纹应为 0")
	}
	if Fingerprint("<html><script>var a=1</script></html>") != 0 {
		t.Error("只有脚本的页面指纹应为 0")
	}
	if Changed(0, 12345, 0) {
		t.Error("缺指纹时不应判定变更")
	}
}

func TestNormalizeIgnoresTags(t *testing.T) {
	a := Fingerprint(`<div class="a"><p>hello world</p></div>`)
	b := Fingerprint(`<span id="x">hello world</span>`)
	if Hamming(a, b) > 3 {
		t.Errorf("仅标签/属性不同，内容相同时距离应很小，实际=%d", Hamming(a, b))
	}
}
