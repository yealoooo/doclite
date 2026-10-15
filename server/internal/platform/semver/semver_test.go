package semver

import "testing"

func TestParse(t *testing.T) {
	for _, s := range []string{"1.2.3", " 1.2.3 ", "0.0.1", "1.2.3-beta.1", "1.2.3-rc.1+build.5", "10.20.30+meta"} {
		if _, err := Parse(s); err != nil {
			t.Errorf("Parse(%q) 应该合法：%v", s, err)
		}
	}
	for _, s := range []string{"", "abc", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-01", "1.2.3+", "1.2.3-a..b", "v1.2.3", "1.2.3-é"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) 应该不合法", s)
		}
	}
}

func TestCompare(t *testing.T) {
	// 按优先级从低到高（semver.org 的例子），相邻两个前者低于后者。
	order := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0", "2.0.0",
	}
	for i := 0; i+1 < len(order); i++ {
		a, b := mustParse(t, order[i]), mustParse(t, order[i+1])
		if Compare(a, b) != -1 || Compare(b, a) != 1 {
			t.Errorf("%s 应低于 %s", order[i], order[i+1])
		}
	}
	if Compare(mustParse(t, "1.2.0+build.1"), mustParse(t, "1.2.0+build.2")) != 0 {
		t.Error("构建元数据不参与比较")
	}
}

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
