// Package scanner · 本地产品指纹库
//
// 目的：把「一眼就能认出来」的产品用零成本规则判掉，不要再交给模型。
// 实测（2026-11-09 审计 130 条）：不清晰样本里大量是 ESXi / Spring Boot / Kubelet /
// Tomcat / Lotus Expeditor / SAP MC 这类特征极明显的产品，走一轮抓包 + 2 次 LLM 纯属浪费。
package scanner

import (
	"regexp"
	"strings"
)

// ProductRule 一条本地规则：命中则直接给出产品名
type ProductRule struct {
	Name string
	Re   *regexp.Regexp
}

// 注意顺序：先具体后笼统。命中即返回，不再往下匹配。
var productRules = []ProductRule{
	// 虚拟化 / 存储 / 网络设备
	{"VMware ESXi", regexp.MustCompile(`(?i)(vmware\s*esxi|esxi\s*\d|vmware\s*esx)`)},
	{"VMware vCenter", regexp.MustCompile(`(?i)(vsphere|vcenter)`)},
	{"华为 iBMC", regexp.MustCompile(`(?i)(ibmc|huawei.*bmc)`)},
	{"浪潮 BMC", regexp.MustCompile(`(?i)(inspur|浪潮)`)},
	{"Mellanox MLNX-OS", regexp.MustCompile(`(?i)mellanox|mlnx-os`)},
	{"OceanStor DeviceManager", regexp.MustCompile(`(?i)oceanstor`)},
	{"深信服", regexp.MustCompile(`深信服|sangfor`)},
	{"信锐网络管理系统", regexp.MustCompile(`(?i)信锐|sundray`)},
	{"FutureMatrix", regexp.MustCompile(`(?i)futurematrix`)},
	{"H3C", regexp.MustCompile(`(?i)(h3c|h3c\s*erp)`)},

	// 中间件 / 运行时
	{"Spring Boot", regexp.MustCompile(`(?i)(whitelabel error page|spring boot|spring-boot)`)},
	{"Apache Tomcat", regexp.MustCompile(`(?i)(apache tomcat|tomcat)`)},
	{"Jetty", regexp.MustCompile(`(?i)\bjetty\b`)},
	{"Envoy", regexp.MustCompile(`(?i)\benvoy\b`)},
	{"OpenResty", regexp.MustCompile(`(?i)openresty`)},
	{"nginx", regexp.MustCompile(`(?i)\bnginx\b`)},
	{"Apache httpd", regexp.MustCompile(`(?i)apache/?2\.\d|apache http server`)},
	{"Microsoft IIS", regexp.MustCompile(`(?i)(microsoft-iis|iis windows|iis\s*10)`)},
	{"Kubernetes Kubelet", regexp.MustCompile(`(?i)(kubelet|kubernetes)`)},
	{"Prometheus metrics", regexp.MustCompile(`(?i)(prometheus|go_gc_duration_seconds)`)},
	{"Elasticsearch", regexp.MustCompile(`(?i)(elasticsearch|you know, for search)`)},
	{"Lotus Expeditor", regexp.MustCompile(`(?i)(lotus expeditor|ibm lotus)`)},
	{"SAP Management Console", regexp.MustCompile(`(?i)sap management console|sap netweaver`)},
	{"CAS 单点登录", regexp.MustCompile(`(?i)(central authentication service|\bCAS\b.*登录)`)},
	{"Jenkins", regexp.MustCompile(`(?i)\bjenkins\b`)},
	{"GitLab", regexp.MustCompile(`(?i)\bgitlab\b`)},
	{"Grafana", regexp.MustCompile(`(?i)\bgrafana\b`)},
	{"Zabbix", regexp.MustCompile(`(?i)\bzabbix\b`)},
	{"RabbitMQ", regexp.MustCompile(`(?i)rabbitmq`)},
	{"MinIO", regexp.MustCompile(`(?i)minio`)},
	{"Consul", regexp.MustCompile(`(?i)\bconsul\b`)},
	{"Nacos", regexp.MustCompile(`(?i)\bnacos\b`)},
	{"宝塔面板", regexp.MustCompile(`(?i)(宝塔|bt\.cn|baota)`)},
	{"phpMyAdmin", regexp.MustCompile(`(?i)phpmyadmin`)},
	{"SonarQube", regexp.MustCompile(`(?i)sonarqube`)},
	{"WeBlogic", regexp.MustCompile(`(?i)(weblogic|bea systems)`)},
	{"IBM WebSphere", regexp.MustCompile(`(?i)websphere`)},
	{"JBoss / WildFly", regexp.MustCompile(`(?i)(jboss|wildfly)`)},

	// 常见业务系统
	{"用友 NC", regexp.MustCompile(`(?i)(yonyou|用友)`)},
	{"致远 OA", regexp.MustCompile(`(?i)(seeyon|致远)`)},
	{"泛微 OA", regexp.MustCompile(`(?i)(weaver|泛微|ecology)`)},
	{"金蝶", regexp.MustCompile(`金蝶|kingdee`)},
	{"蓝凌 OA", regexp.MustCompile(`(?i)(landray|蓝凌)`)},
	{"Coremail 邮件", regexp.MustCompile(`(?i)coremail`)},
	{"Exchange", regexp.MustCompile(`(?i)(outlook web app|exchange)`)},
}

// MatchProduct 用本地规则匹配产品名，命中返回产品名，未命中返回空串。
// text 建议传「标题 + 指纹 + Banner + Server 头 + 页面首屏」的拼接。
func MatchProduct(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for _, r := range productRules {
		if r.Re.MatchString(text) {
			return r.Name
		}
	}
	return ""
}
