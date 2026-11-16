package scanner

import "testing"

// TestPrioritizePortsKeepsSet 排序不得增删端口：启发式只改先后，覆盖范围必须一模一样
func TestPrioritizePortsKeepsSet(t *testing.T) {
	in := []int{1, 22, 80, 443, 445, 3389, 65535, 5432, 70000}
	got := prioritizePorts(in)
	if len(got) != len(in) {
		t.Fatalf("长度变了: %d -> %d", len(in), len(got))
	}
	seen := map[int]int{}
	for _, p := range got {
		seen[p]++
	}
	for _, p := range in {
		if seen[p] != 1 {
			t.Fatalf("端口 %d 出现 %d 次（应为 1 次），结果 %v", p, seen[p], got)
		}
	}
}

// TestPrioritizePortsHighValueFirst 高价值端口必须排在前面
func TestPrioritizePortsHighValueFirst(t *testing.T) {
	in := []int{65535, 12345, 9999, 3306, 22, 80}
	got := prioritizePorts(in)
	// 22/80/3306 都是权重>=90 的，必须在前 3 位
	head := got[:3]
	want := map[int]bool{22: true, 80: true, 3306: true}
	for _, p := range head {
		if !want[p] {
			t.Fatalf("前三应是高价值端口，得到 %v（完整 %v）", head, got)
		}
	}
	// 权重为 0 的保持原始相对顺序
	if got[3] != 65535 || got[4] != 12345 || got[5] != 9999 {
		t.Errorf("低权重端口相对顺序被破坏: %v", got)
	}
}

// TestPrioritizePortsStable 同权重不打乱，保证两次扫描日志可比
func TestPrioritizePortsStable(t *testing.T) {
	in := []int{8080, 8000, 8008, 80}
	got := prioritizePorts(in)
	a, b := -1, -1
	for i, p := range got {
		if p == 8080 {
			a = i
		}
		if p == 80 {
			b = i
		}
	}
	// 8080 权重 100 与 80 相同，且原序列中 8080 在前 -> 结果里也应在前
	if a > b {
		t.Errorf("同权重端口被打乱: %v", got)
	}
}

// TestHighValuePortCount 计数只数命中权重表的
func TestHighValuePortCount(t *testing.T) {
	if n := highValuePortCount([]int{22, 80, 65535, 12345}); n != 2 {
		t.Errorf("期望 2，得到 %d", n)
	}
}
