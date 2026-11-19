package service

import (
	"log"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/target"
)

// PurgeExcludedData 把落进免扫描范围的存量数据删掉。
// 语义：既然这个范围"不要扫"，之前扫出来的东西也不该继续出现在资产/服务里。
// 删除范围：services + assets（assets 级联删除页面快照与合规结果）；
// **不删 scan_snapshots**——那是 IP 历史趋势的核心数据。
// 注意：同 IP 的多条归属记录一并删除（IP 都被排除了，归属也失去意义）。
func PurgeExcludedData(patterns []string) (assets int64, services int64) {
	if config.DB == nil || len(patterns) == 0 {
		return
	}
	ex := target.NewExcluder(patterns)

	ips := map[string]bool{}
	var svcIPs, assetIPs []string
	// 用 Raw 取文本形式的 IP：gorm 的 Pluck 会把 "ip::text" 当普通列名，拼出来的 SQL 非法
	config.DB.Raw("SELECT DISTINCT host(ip) FROM services").Scan(&svcIPs)
	config.DB.Raw("SELECT DISTINCT host(ip) FROM assets").Scan(&assetIPs)
	for _, ip := range append(svcIPs, assetIPs...) {
		if ip != "" && ip != "<nil>" && ex.Contains(ip) {
			ips[ip] = true
		}
	}
	if len(ips) == 0 {
		return
	}
	var list []string
	for ip := range ips {
		list = append(list, ip)
	}
	r1 := config.DB.Where("ip IN ?", list).Delete(&model.Service{})
	r2 := config.DB.Where("ip IN ?", list).Delete(&model.Asset{})
	config.DB.Where("ip IN ?", list).Delete(&model.HostState{})
	if r1.Error != nil {
		log.Printf("[exclude] 删除服务失败: %v", r1.Error)
	}
	if r2.Error != nil {
		log.Printf("[exclude] 删除资产失败: %v", r2.Error)
	}
	log.Printf("[exclude] 排除范围清理：IP %d 个，服务 %d 条，资产 %d 条", len(list), r1.RowsAffected, r2.RowsAffected)
	return r2.RowsAffected, r1.RowsAffected
}
