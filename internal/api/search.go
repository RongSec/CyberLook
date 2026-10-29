package api

import (
	"strings"

	"gorm.io/gorm"
)

// SearchCond 前端 SPL 检索条件
type SearchCond struct {
	Field string      `json:"field"`
	Op    string      `json:"op"`
	Value interface{} `json:"value"` // string 或多选时的 []string
	Logic string      `json:"logic"` // AND / OR
}

// Str 取单值字符串（多选场景返回空）
func (c SearchCond) Str() string {
	if v, ok := c.Value.(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// values 把 Value 统一成 []string（单值也包成一片）
func (c SearchCond) values() []string {
	switch v := c.Value.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

// ApplySearchConds 把条件数组转成 GORM 查询。
// allowed 是「前端字段名 → 真实列名」白名单，未列入的字段忽略（防注入）。
func ApplySearchConds(q *gorm.DB, conds []SearchCond, allowed map[string]string) *gorm.DB {
	first := true
	for _, c := range conds {
		col, ok := allowed[c.Field]
		if !ok {
			continue
		}
		vals := c.values()
		if len(vals) == 0 {
			continue
		}
		v := vals[0]
		var expr string
		var arg interface{}
		switch c.Op {
		case "eq":
			expr, arg = col+"::text = ?", v
		case "neq":
			expr, arg = col+"::text <> ?", v
		case "like":
			expr, arg = col+"::text ILIKE ?", "%"+v+"%"
		case "prefix":
			expr, arg = col+"::text ILIKE ?", v+"%"
		case "gt":
			expr, arg = col+"::text > ?", v
		case "lt":
			expr, arg = col+"::text < ?", v
		default:
			expr, arg = col+"::text ILIKE ?", "%"+v+"%"
		}
		// 多选：等于/不等于 用 IN / NOT IN
		if len(vals) > 1 && (c.Op == "eq" || c.Op == "neq") {
			if c.Op == "eq" {
				expr, arg = col+"::text IN ?", vals
			} else {
				expr, arg = col+"::text NOT IN ?", vals
			}
		}
		if first {
			q = q.Where(expr, arg)
			first = false
			continue
		}
		if strings.EqualFold(c.Logic, "OR") {
			q = q.Or(expr, arg)
		} else {
			q = q.Where(expr, arg)
		}
	}
	return q
}
