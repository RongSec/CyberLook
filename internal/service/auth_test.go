package service

import "testing"

func TestValidatePasswordStrength(t *testing.T) {
	cases := []struct {
		pwd  string
		want bool
	}{
		{"123456", false},          // 太短
		{"12345678", false},        // 只有数字（1 类）
		{"abcdefgh", false},        // 只有小写（1 类）
		{"abcdefgh1", false},       // 小写+数字（2 类）
		{"Abc12345", false},        // 命中弱口令表 abc12345
		{"abcdefg1!", true},        // 小写+数字+符号（3 类）
		{"Hu42704270.,.,", true},   // 当前密码（4 类）
		{"password", false},        // 弱口令
		{"Password1", false},       // 弱口令（大小写不敏感匹配）
	}
	for _, c := range cases {
		err := validatePasswordStrength(c.pwd)
		got := err == nil
		if got != c.want {
			t.Errorf("%q: want ok=%v, got ok=%v (err=%v)", c.pwd, c.want, got, err)
		} else {
			t.Logf("%q -> ok=%v %v", c.pwd, got, err)
		}
	}
}
