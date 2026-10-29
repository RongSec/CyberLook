-- 节点级 Tscan License（内容由配置页上传，agent 心跳时拉取写入 config/License.lic）
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS license text DEFAULT '';
