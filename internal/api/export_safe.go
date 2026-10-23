package api

// safeCell 防止 Excel/CSV 公式注入
// 以 = + - @ 开头的单元格会被 Excel 当作公式执行（如 =cmd|'/C calc'!A0）
// 前置单引号强制为文本
func safeCell(v interface{}) interface{} {
	s, ok := v.(string)
	if !ok || s == "" {
		return v
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return v
}
