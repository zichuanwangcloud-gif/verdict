package linear

import (
	"encoding/json"
	"testing"
)

func TestNumberPrefixRanges(t *testing.T) {
	cases := map[string][]string{
		"1":   {`{"number":{"gte":1,"lte":1}}`, `{"number":{"gte":10,"lte":19}}`, `{"number":{"gte":100,"lte":199}}`},
		"14":  {`{"number":{"gte":14,"lte":14}}`, `{"number":{"gte":140,"lte":149}}`, `{"number":{"gte":1400,"lte":1499}}`},
		"142": {`{"number":{"gte":142,"lte":142}}`, `{"number":{"gte":1420,"lte":1429}}`},
	}
	for digits, want := range cases {
		r := numberPrefixRanges(digits)
		if len(r) < len(want) {
			t.Fatalf("%s: 区间太少 %v", digits, r)
		}
		for i, w := range want {
			b, _ := json.Marshal(r[i])
			if string(b) != w {
				t.Fatalf("%s 第 %d 段: got %s want %s", digits, i, b, w)
			}
		}
	}
	if numberPrefixRanges("") != nil {
		t.Fatal("空数字应返回 nil（由调用方退回全文搜索）")
	}
}
